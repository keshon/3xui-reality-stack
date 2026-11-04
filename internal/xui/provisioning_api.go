package xui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type provisionedAttachment struct {
	Client     provisionedClient `json:"client"`
	InboundIDs []int             `json:"inboundIds"`
}

type provisioningCatalog struct {
	Inbounds []provisionedInbound
	Clients  []provisionedAttachment
}

func (s *panelSession) provisioningCatalog(ctx context.Context) (provisioningCatalog, error) {
	var result provisioningCatalog
	var rows []map[string]json.RawMessage
	if err := s.json(ctx, "/panel/api/inbounds/list", nil, &rows); err != nil {
		return result, err
	}
	for _, row := range rows {
		// List responses expose parsed configuration objects; write/detail
		// payloads use JSON strings. Normalize only at this API boundary.
		for _, key := range []string{"settings", "streamSettings", "sniffing", "allocate"} {
			if raw := bytes.TrimSpace(row[key]); len(raw) > 0 && raw[0] == '{' {
				row[key], _ = json.Marshal(string(raw))
			}
		}
		data, _ := json.Marshal(row)
		var inbound provisionedInbound
		if json.Unmarshal(data, &inbound) != nil {
			return result, errors.New("invalid provisioning inbound response")
		}
		result.Inbounds = append(result.Inbounds, inbound)
	}
	if err := s.json(ctx, "/panel/api/clients/export", nil, &result.Clients); err != nil {
		return result, err
	}
	return result, nil
}

// Dynamic client lists are verified through the unified export instead. API
// defaults and runtime counters do not change installation identity.
func containsProvisioningJSON(observed, expected string, clientLists bool) bool {
	decode := func(raw string) (any, error) {
		if !json.Valid([]byte(raw)) {
			return nil, errors.New("invalid provisioning JSON")
		}
		var result any
		d := json.NewDecoder(bytes.NewBufferString(raw))
		d.UseNumber()
		err := d.Decode(&result)
		return result, err
	}
	a, errA := decode(observed)
	b, errB := decode(expected)
	if errA != nil || errB != nil {
		return false
	}
	if clientLists {
		object, ok := b.(map[string]any)
		if !ok {
			return false
		}
		delete(object, "clients")
		delete(object, "users")
	}
	var contains func(any, any) bool
	contains = func(a, b any) bool {
		if object, ok := b.(map[string]any); ok {
			other, ok := a.(map[string]any)
			if !ok {
				return false
			}
			for key, value := range object {
				observed, exists := other[key]
				if !exists || !contains(observed, value) {
					return false
				}
			}
			return true
		}
		return reflect.DeepEqual(a, b)
	}
	return contains(a, b)
}

func sameProvisionedInbound(actual, want provisionedInbound) bool {
	return actual.ID > 0 && actual.Remark == want.Remark && actual.Enable == want.Enable && actual.Listen == want.Listen && actual.Port == want.Port && actual.Protocol == want.Protocol && (actual.NodeID == nil || *actual.NodeID == 0) && containsProvisioningJSON(actual.Settings, want.Settings, true) && containsProvisioningJSON(actual.StreamSettings, want.StreamSettings, false) && containsProvisioningJSON(actual.Sniffing, want.Sniffing, false)
}

func (p *provisioningIntent) checkCatalog(c config, catalog provisioningCatalog) (map[string]int, error) {
	wanted, err := p.inbounds(c)
	if err != nil {
		return nil, err
	}
	byName := map[string]provisionedInbound{}
	for _, row := range wanted {
		byName[row.Remark] = row
	}
	found := map[string]int{}
	ids := map[int]bool{}
	for _, row := range catalog.Inbounds {
		want, ok := byName[row.Remark]
		if !ok || found[row.Remark] != 0 || ids[row.ID] || !sameProvisionedInbound(row, want) {
			return nil, errors.New("unexpected or changed inbound blocks provisioning")
		}
		if saved := p.InboundIDs[row.Remark]; saved != 0 && saved != row.ID {
			return nil, errors.New("recorded provisioning inbound was replaced")
		}
		found[row.Remark] = row.ID
		ids[row.ID] = true
	}
	for name, id := range p.InboundIDs {
		if found[name] != id {
			return nil, errors.New("recorded provisioning inbound disappeared")
		}
	}
	clients := map[string]provisionedClient{}
	for _, row := range p.Clients {
		clients[row.Email] = row
	}
	seen := map[string]bool{}
	for _, row := range catalog.Clients {
		want, ok := clients[row.Client.Email]
		if !ok || seen[row.Client.Email] || row.Client != want {
			return nil, errors.New("unexpected or changed account blocks provisioning")
		}
		seen[row.Client.Email] = true
		attached := map[int]bool{}
		for _, id := range row.InboundIDs {
			if !ids[id] || attached[id] {
				return nil, errors.New("unexpected account attachment blocks provisioning")
			}
			attached[id] = true
		}
		if p.AppliedClients[row.Client.Email] && len(attached) != len(wanted) {
			return nil, errors.New("recorded account attachments changed")
		}
	}
	for name := range p.AppliedClients {
		if !seen[name] {
			return nil, errors.New("recorded provisioning account disappeared")
		}
	}
	return found, nil
}

// Requires an operation-owned isolated panel and the deployment lock. guard
// rechecks ownership before each API write. This is not an adoption workflow.
func provisionFreshPanel(ctx context.Context, dir string, c config, s *panelSession, guard func() error) error {
	if guard == nil {
		return errors.New("provisioning requires an ownership guard")
	}
	if err := guard(); err != nil {
		return err
	}
	file := filepath.Join(dir, "provisioning.json")
	p := new(provisioningIntent)
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		catalog, err := s.provisioningCatalog(ctx)
		if err != nil {
			return err
		}
		if len(catalog.Inbounds) != 0 || len(catalog.Clients) != 0 {
			return errors.New("fresh provisioning requires an empty panel catalog")
		}
		p, err = newProvisioningIntent(c, rand.Reader)
		if err != nil {
			return err
		}
		if err := deployment.WriteJSON(file, p); err != nil {
			return err
		}
	} else {
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			return errors.New("invalid private provisioning state")
		}
		if ok, err := readJSON(file, p, true); err != nil || !ok {
			return errors.New("cannot read original provisioning intent")
		}
	}
	wanted, err := p.inbounds(c)
	if err != nil {
		return err
	}
	validPending := p.Pending == ""
	for _, row := range wanted {
		validPending = validPending || p.Pending == "inbound:"+row.Remark
	}
	for _, row := range p.Clients {
		validPending = validPending || p.Pending == "client:"+row.Email
	}
	if !validPending {
		return errors.New("invalid pending provisioning write")
	}
	save := func() error { return deployment.WriteJSON(file, p) }
	read := func() (provisioningCatalog, map[string]int, error) {
		catalog, err := s.provisioningCatalog(ctx)
		if err != nil {
			return catalog, nil, err
		}
		ids, err := p.checkCatalog(c, catalog)
		return catalog, ids, err
	}
	write := func(key, path string, body any, replayAttachment bool) error {
		if p.Pending != "" && !(replayAttachment && p.Pending == key) {
			return errors.New("previous provisioning write is unresolved; inspect before retrying")
		}
		if err := guard(); err != nil {
			return err
		}
		p.Pending = key
		if err := save(); err != nil {
			return err
		}
		// Read-back is authoritative even if the write reply was lost.
		_ = s.json(ctx, path, body, nil)
		return nil
	}
	for _, row := range wanted {
		_, ids, err := read()
		if err != nil {
			return err
		}
		if ids[row.Remark] == 0 {
			if err := write("inbound:"+row.Remark, "/panel/api/inbounds/add", row, false); err != nil {
				return err
			}
			_, ids, err = read()
			if err != nil {
				return err
			}
			if ids[row.Remark] == 0 {
				return errors.New("inbound creation is not confirmed")
			}
		}
		p.InboundIDs[row.Remark] = ids[row.Remark]
		if p.Pending == "inbound:"+row.Remark {
			p.Pending = ""
		}
		if err := save(); err != nil {
			return err
		}
	}
	target := make([]int, 0, len(wanted))
	for _, row := range wanted {
		target = append(target, p.InboundIDs[row.Remark])
	}
	slices.Sort(target)
	for _, client := range p.Clients {
		catalog, _, err := read()
		if err != nil {
			return err
		}
		attached := func(catalog provisioningCatalog) bool {
			for _, row := range catalog.Clients {
				if row.Client.Email == client.Email {
					ids := slices.Clone(row.InboundIDs)
					slices.Sort(ids)
					return slices.Equal(ids, target)
				}
			}
			return false
		}
		if !attached(catalog) {
			if p.AppliedClients[client.Email] {
				return errors.New("recorded account attachments changed")
			}
			path := "/panel/api/clients/add"
			var body any = provisionedAttachment{client, target}
			existing := false
			for _, row := range catalog.Clients {
				if row.Client.Email == client.Email {
					existing = true
				}
			}
			// checkCatalog already proved the stored credentials. Native attach
			// only completes links to our exact inbounds; it creates no identity.
			if existing {
				path = "/panel/api/clients/attach/" + client.Email
				body = map[string]any{"inboundIds": target}
			}
			if err := write("client:"+client.Email, path, body, existing); err != nil {
				return err
			}
			catalog, _, err = read()
			if err != nil {
				return err
			}
			if !attached(catalog) {
				return errors.New("account creation or attachments are not confirmed")
			}
		}
		p.AppliedClients[client.Email] = true
		if p.Pending == "client:"+client.Email {
			p.Pending = ""
		}
		if err := save(); err != nil {
			return err
		}
	}
	if err := guard(); err != nil {
		return err
	}
	_, _, err = read()
	return err
}

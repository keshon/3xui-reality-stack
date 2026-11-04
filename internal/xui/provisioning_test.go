package xui

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func provisioningConfiguration(t *testing.T) config {
	t.Helper()
	c, _, err := initialConfiguration(rawInitialInput(t, validInitialInput()), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProvisioningUsesSharedAccountsAndValidRealityKeys(t *testing.T) {
	c := provisioningConfiguration(t)
	p, err := newProvisioningIntent(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.inbounds(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(p.Clients) != 2 || rows[0].Port != 443 || rows[1].Port != 12812 || rows[2].Port != 443 || rows[2].Protocol != "hysteria" {
		t.Fatal("core transport layout changed")
	}
	var stream struct {
		Reality struct {
			Private  string `json:"privateKey"`
			Settings struct {
				Public string `json:"publicKey"`
			} `json:"settings"`
		} `json:"realitySettings"`
	}
	if json.Unmarshal([]byte(rows[0].StreamSettings), &stream) != nil {
		t.Fatal("invalid Reality settings")
	}
	private, _ := base64.RawURLEncoding.DecodeString(stream.Reality.Private)
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil || base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != stream.Reality.Settings.Public {
		t.Fatal("Reality public and private keys do not match")
	}
	if !strings.Contains(rows[0].Settings, `"encryption":"none"`) || !strings.Contains(rows[2].Settings, `"version":2`) || !strings.Contains(rows[2].StreamSettings, `"network":"hysteria"`) {
		t.Fatal("required client/core compatibility fields are absent")
	}
	for _, row := range rows {
		if !strings.Contains(row.Sniffing, `"routeOnly":true`) || !strings.Contains(row.StreamSettings, c.Env["PUBLIC_DOMAIN"]) {
			t.Fatal("routing or public connection address changed")
		}
	}
	if _, err := newProvisioningIntent(c, bytes.NewReader(nil)); err == nil {
		t.Fatal("entropy failure did not stop provisioning")
	}
}

type provisioningFixture struct {
	mu                    sync.Mutex
	catalog               provisioningCatalog
	writes                int
	failRead, ignoreWrite bool
	partialClient         bool
	beforeWrite           func()
	settings              map[string]json.RawMessage
	coreState             string
	emptyCatalogNull      bool
	objectInbounds        bool
	routingConfig         any
}

func newProvisioningFixture(t *testing.T) (*provisioningFixture, *panelSession) {
	t.Helper()
	f := &provisioningFixture{catalog: provisioningCatalog{Inbounds: []provisionedInbound{}, Clients: []provisionedAttachment{}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reply := func(value any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": value}) }
		if r.URL.Path == "/csrf-token" && r.Method == "GET" {
			reply("token")
			return
		}
		if r.URL.Path == "/getTwoFactorEnable" {
			reply(false)
			return
		}
		if r.URL.Path == "/login" {
			reply(nil)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/add") || strings.HasPrefix(r.URL.Path, "/panel/api/clients/attach/") {
			if r.Method != "POST" || r.Header.Get("X-CSRF-TOKEN") != "token" {
				t.Error("API write bypassed CSRF contract")
				http.Error(w, "invalid", 403)
				return
			}
			if f.beforeWrite != nil {
				f.beforeWrite()
			}
			f.writes++
			if f.ignoreWrite {
				http.Error(w, "ambiguous", 503)
				return
			}
			switch r.URL.Path {
			case "/panel/api/inbounds/add":
				var row provisionedInbound
				if json.NewDecoder(r.Body).Decode(&row) != nil {
					t.Error("invalid inbound payload")
				}
				row.ID = 10 + len(f.catalog.Inbounds)
				f.catalog.Inbounds = append(f.catalog.Inbounds, row)
			case "/panel/api/clients/add":
				var row provisionedAttachment
				if json.NewDecoder(r.Body).Decode(&row) != nil {
					t.Error("invalid unified account payload")
				}
				if f.partialClient {
					row.InboundIDs = row.InboundIDs[:1]
				}
				f.catalog.Clients = append(f.catalog.Clients, row)
			default:
				if !strings.HasPrefix(r.URL.Path, "/panel/api/clients/attach/") {
					t.Error("unexpected provisioning endpoint")
					return
				}
				var body struct {
					InboundIDs []int `json:"inboundIds"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid attachment payload")
				}
				name := strings.TrimPrefix(r.URL.Path, "/panel/api/clients/attach/")
				for i := range f.catalog.Clients {
					if f.catalog.Clients[i].Client.Email == name {
						f.catalog.Clients[i].InboundIDs = body.InboundIDs
					}
				}
			}
			// Every committed write deliberately loses its successful response.
			http.Error(w, "lost response", 503)
			return
		}
		if f.failRead {
			f.failRead = false
			http.Error(w, "temporary", 503)
			return
		}
		switch r.URL.Path {
		case "/panel/api/setting/all":
			reply(f.settings)
		case "/panel/api/server/status":
			reply(map[string]any{"xray": map[string]any{"state": f.coreState, "errorMsg": ""}})
		case "/panel/api/server/getConfigJson":
			if f.routingConfig != nil {
				reply(f.routingConfig)
			} else {
				reply(map[string]any{"routing": json.RawMessage(freshRouting), "outbounds": json.RawMessage(freshOutbounds)})
			}
		case "/panel/api/server/getDb":
			_, _ = w.Write(append([]byte("SQLite format 3\x00"), make([]byte, 100)...))
		case "/panel/api/inbounds/list":
			if f.objectInbounds && len(f.catalog.Inbounds) > 0 {
				var rows []map[string]any
				for _, inbound := range f.catalog.Inbounds {
					data, _ := json.Marshal(inbound)
					var row map[string]any
					_ = json.Unmarshal(data, &row)
					for _, key := range []string{"settings", "streamSettings", "sniffing"} {
						var value any
						if json.Unmarshal([]byte(row[key].(string)), &value) != nil {
							t.Error("invalid fixture object")
							http.Error(w, "invalid fixture object", http.StatusInternalServerError)
							return
						}
						row[key] = value
					}
					row["allocate"] = nil
					rows = append(rows, row)
				}
				reply(rows)
				break
			}
			if f.emptyCatalogNull && len(f.catalog.Inbounds) == 0 {
				reply(nil)
			} else {
				reply(f.catalog.Inbounds)
			}
		case "/panel/api/clients/export":
			if f.emptyCatalogNull && len(f.catalog.Clients) == 0 {
				reply(nil)
			} else {
				reply(f.catalog.Clients)
			}
		default:
			t.Error("unexpected provisioning read")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return f, &panelSession{base: server.URL, http: server.Client()}
}

func TestProvisioningCompletesPartialNativeAttachments(t *testing.T) {
	c := provisioningConfiguration(t)
	f, s := newProvisioningFixture(t)
	dir := t.TempDir()
	guard := func() error { return nil }
	f.partialClient = true
	if err := provisionFreshPanel(t.Context(), dir, c, s, guard); err == nil {
		t.Fatal("partial account attachment was accepted")
	}
	first := f.catalog.Clients[0].Client
	for range 3 {
		if provisionFreshPanel(t.Context(), dir, c, s, guard) == nil {
			break
		}
	}
	if len(f.catalog.Clients) != 2 || f.catalog.Clients[0].Client != first || f.writes != 7 {
		t.Fatal("attachment recovery recreated or changed an account")
	}
	for _, row := range f.catalog.Clients {
		if len(row.InboundIDs) != 3 {
			t.Fatal("native attachment recovery did not finish all lanes")
		}
	}
}

func TestProvisioningRecoversCommittedWritesWithoutRotatingCredentials(t *testing.T) {
	c := provisioningConfiguration(t)
	f, s := newProvisioningFixture(t)
	f.emptyCatalogNull = true
	f.objectInbounds = true
	dir := t.TempDir()
	guard := func() error { return nil }
	f.beforeWrite = func() {
		var p provisioningIntent
		if ok, err := readJSON(filepath.Join(dir, "provisioning.json"), &p, true); err != nil || !ok || p.Pending == "" {
			t.Error("API write preceded durable credentials/write intent")
		}
		f.failRead = true
	}
	for tries := 0; tries < 6; tries++ {
		err := provisionFreshPanel(t.Context(), dir, c, s, guard)
		if err == nil {
			break
		}
		if tries == 5 {
			t.Fatal(err)
		}
	}
	if f.writes != 5 || len(f.catalog.Clients) != 2 {
		t.Fatal("recovery duplicated transports or accounts")
	}
	before, err := os.ReadFile(filepath.Join(dir, "provisioning.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionFreshPanel(t.Context(), dir, c, s, guard); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "provisioning.json"))
	if !bytes.Equal(before, after) || f.writes != 5 {
		t.Fatal("replay rotated credentials or wrote the panel")
	}
	for _, row := range f.catalog.Clients {
		if len(row.InboundIDs) != 3 || row.Client.Password == "" || row.Client.Auth == "" || row.Client.SubID == "" {
			t.Fatal("one account was not attached to all three lanes")
		}
	}
}

func TestProvisioningRefusesDriftAndUnresolvedWrites(t *testing.T) {
	for _, fault := range []string{"foreign", "inbound-id", "private-key", "account-password", "attachments", "missing-inbound", "missing-account", "configuration", "invalid-state"} {
		t.Run(fault, func(t *testing.T) {
			c := provisioningConfiguration(t)
			f, s := newProvisioningFixture(t)
			dir := t.TempDir()
			guard := func() error { return nil }
			if err := provisionFreshPanel(t.Context(), dir, c, s, guard); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "foreign":
				f.catalog.Inbounds = append(f.catalog.Inbounds, provisionedInbound{ID: 99, Remark: "foreign"})
			case "inbound-id":
				f.catalog.Inbounds[0].ID = 99
			case "private-key":
				f.catalog.Inbounds[0].StreamSettings = `{}`
			case "account-password":
				f.catalog.Clients[0].Client.Password = "changed"
			case "attachments":
				f.catalog.Clients[0].InboundIDs = f.catalog.Clients[0].InboundIDs[:1]
			case "missing-inbound":
				f.catalog.Inbounds = f.catalog.Inbounds[1:]
			case "missing-account":
				f.catalog.Clients = f.catalog.Clients[1:]
			case "configuration":
				c.Users = []string{"changed"}
			case "invalid-state":
				if err := os.WriteFile(filepath.Join(dir, "provisioning.json"), []byte(`{"private_key":"private-value"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := provisionFreshPanel(t.Context(), dir, c, s, guard); err == nil || strings.Contains(err.Error(), "private-value") {
				t.Fatal("drift was accepted or secret echoed")
			}
			if f.writes != 5 {
				t.Fatal("drift triggered panel repairs")
			}
		})
	}
	c := provisioningConfiguration(t)
	f, s := newProvisioningFixture(t)
	dir := t.TempDir()
	f.ignoreWrite = true
	for range 2 {
		if err := provisionFreshPanel(context.Background(), dir, c, s, func() error { return nil }); err == nil {
			t.Fatal("unresolved write was considered complete")
		}
	}
	if f.writes != 1 {
		t.Fatal("an ambiguous unconfirmed create was repeated")
	}
	before := f.writes
	if err := provisionFreshPanel(t.Context(), dir, c, s, func() error { return errors.New("ownership changed") }); err == nil || f.writes != before {
		t.Fatal("ownership guard was bypassed")
	}
}

func TestProvisioningNeverAdoptsAnExistingCatalog(t *testing.T) {
	c := provisioningConfiguration(t)
	f, s := newProvisioningFixture(t)
	dir := t.TempDir()
	f.catalog.Clients = append(f.catalog.Clients, provisionedAttachment{Client: provisionedClient{Email: "existing"}})
	if err := provisionFreshPanel(t.Context(), dir, c, s, func() error { return nil }); err == nil {
		t.Fatal("fresh provisioning adopted an existing account")
	}
	if f.writes != 0 {
		t.Fatal("existing catalog was modified")
	}
	if _, err := os.Stat(filepath.Join(dir, "provisioning.json")); !os.IsNotExist(err) {
		t.Fatal("existing identities were replaced with a new intent")
	}
	p, err := newProvisioningIntent(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := p.inbounds(c)
	copyRows, _ := p.inbounds(c)
	if !reflect.DeepEqual(rows, copyRows) {
		t.Fatal("rendering changed frozen transport identities")
	}
}

func TestPrivateProvisioningRechecksContainerOwnership(t *testing.T) {
	w, docker, _ := panelWorkflowFixture(t)
	engine := deployment.Engine{Root: w.root}
	if err := engine.Start(t.Context(), panelBootstrapBoundary{w, false}); err == nil {
		t.Fatal("expected unfinished verification stage")
	}
	status, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.root, ".stack/go-operations", status.ID)
	unlock, err := deployment.Lock(w.root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	snapshot, err := w.open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, api := newProvisioningFixture(t)
	w.panelEndpoint = func(webContainer, string) (*Panel, error) { return NewPanel(api.base) }
	f.beforeWrite = func() { docker.panel.Traefik = "true"; docker.savePanel() }
	if err := w.provisionPanel(t.Context(), dir, snapshot); err == nil {
		t.Fatal("changed routing did not stop provisioning")
	}
	if f.writes != 1 {
		t.Fatal("provisioning continued writing after losing isolation")
	}
	docker.panel.Traefik = "false"
	docker.savePanel()
	f.beforeWrite = nil
	if err := w.provisionPanel(t.Context(), dir, snapshot); err != nil {
		t.Fatal(err)
	}
	if f.writes != 5 || docker.creates != 1 || docker.restarts != 1 {
		t.Fatal("ownership recovery duplicated resources or identities")
	}
}

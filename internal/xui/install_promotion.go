package xui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type promotionIntent struct {
	Version          int           `json:"version"`
	Owner            string        `json:"owner"`
	Before           panelIdentity `json:"before"`
	ImageID          string        `json:"image_id"`
	Reference        string        `json:"reference"`
	BackupHash       string        `json:"backup_hash"`
	PreservationHash string        `json:"preservation_hash"`
}

func promotionOverride(snapshot installRuntimeSnapshot, image string) map[string]any {
	return map[string]any{"services": map[string]any{"3x-ui": map[string]any{"image": image, "labels": map[string]string{"io.stackctl.installation": installationOwner(snapshot.Root), "io.stackctl.operation": snapshot.Owner}}}}
}

func promotionDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func privatePromotionFile(dir, name string, limit int64) ([]byte, error) {
	file := filepath.Join(dir, name)
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		return nil, errors.New("invalid private promotion recovery file")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, errors.New("cannot open promotion recovery file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("cannot read promotion recovery file")
	}
	return data, nil
}

func panelCoreReady(ctx context.Context, s *panelSession) error {
	var status struct {
		Xray struct {
			State string `json:"state"`
			Error string `json:"errorMsg"`
		} `json:"xray"`
	}
	if err := s.json(ctx, "/panel/api/server/status", nil, &status); err != nil {
		return err
	}
	if status.Xray.State != "running" || status.Xray.Error != "" {
		return errors.New("panel core is not ready for promotion")
	}
	return nil
}

// Docker startup precedes both HTTP listener readiness and Traefik discovery.
// Retry authenticated reads during that window, without retrying configuration
// writes or retrying credentials that the panel explicitly rejected.
func waitPromotedPanel(ctx context.Context, p *Panel, c config) (*panelSession, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var session *panelSession
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		if session == nil {
			session, err = p.login(ctx, c.Env["PANEL_USER"], c.Env["PANEL_PASS"])
			if errors.Is(err, errCredentials) {
				return nil, err
			}
		}
		if err == nil && session != nil && panelCoreReady(ctx, session) == nil {
			return session, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("published panel route or core readiness timed out")
		case <-ticker.C:
		}
	}
}

func (w *installRuntime) promotionIntent(ctx context.Context, dir string, snapshot installRuntimeSnapshot, web webIntent, panel panelIntent, c config) (promotionIntent, error) {
	var intent promotionIntent
	exists, err := readJSON(filepath.Join(dir, "promotion-intent.json"), &intent, true)
	if err != nil {
		return intent, err
	}
	var before panelIdentity
	if ok, err := readJSON(filepath.Join(dir, "panel-restarted.json"), &before, true); err != nil || !ok {
		return intent, errors.New("panel initialization receipt unavailable for promotion")
	}
	if !exists {
		data, err := privatePromotionFile(dir, "provisioning.json", 2<<20)
		if err != nil {
			return intent, err
		}
		var plan provisioningIntent
		if json.Unmarshal(data, &plan) != nil || plan.Pending != "" || len(plan.InboundIDs) != 3 || len(plan.AppliedClients) != len(c.Users) {
			return intent, errors.New("panel provisioning is not complete")
		}
		found, err := w.observeInstallServices(ctx, snapshot, web, dir, &panel)
		if err != nil {
			return intent, err
		}
		current := found[panelContainerName(snapshot.Owner)]
		if !current.Running || identityOfPanel(current) != before {
			return intent, errors.New("bootstrap panel changed before promotion")
		}
		p, err := w.privatePanelEndpoint(current, c.Env["PANEL_PATH"])
		if err != nil {
			return intent, err
		}
		defer p.transport.CloseIdleConnections()
		var s *panelSession
		if w.panelEndpoint == nil {
			s, err = waitPromotedPanel(ctx, p, c)
		} else {
			s, err = p.login(ctx, c.Env["PANEL_USER"], c.Env["PANEL_PASS"])
		}
		if err != nil {
			return intent, err
		}
		catalog, err := s.provisioningCatalog(ctx)
		if err != nil {
			return intent, err
		}
		if _, err := plan.checkCatalog(c, catalog); err != nil {
			return intent, err
		}
		if err := panelCoreReady(ctx, s); err != nil {
			return intent, err
		}
		preserved, err := s.preservation(ctx)
		if err != nil {
			return intent, err
		}
		backup, err := s.database(ctx)
		if err != nil {
			return intent, err
		}
		found, err = w.observeInstallServices(ctx, snapshot, web, dir, &panel)
		if err != nil || identityOfPanel(found[panelContainerName(snapshot.Owner)]) != before {
			return intent, errors.New("bootstrap changed while capturing promotion recovery")
		}
		if err := deployment.WriteFile(filepath.Join(dir, "panel-before-promotion.db"), backup); err != nil {
			return intent, err
		}
		if err := deployment.WriteFile(filepath.Join(dir, "panel-before-promotion.json"), preserved); err != nil {
			return intent, err
		}
		if err := deployment.WriteJSON(filepath.Join(dir, "xui-promotion.json"), promotionOverride(snapshot, panel.Reference)); err != nil {
			return intent, err
		}
		intent = promotionIntent{1, snapshot.Owner, before, panel.ImageID, panel.Reference, promotionDigest(backup), promotionDigest(preserved)}
		if err := deployment.WriteJSON(filepath.Join(dir, "promotion-intent.json"), intent); err != nil {
			return intent, err
		}
	}
	if intent.Version != 1 || intent.Owner != snapshot.Owner || intent.Before != before || intent.ImageID != panel.ImageID || intent.Reference != panel.Reference {
		return intent, errors.New("promotion intent does not match the original installation")
	}
	for _, file := range []struct {
		name, hash string
		limit      int64
	}{{"panel-before-promotion.db", intent.BackupHash, 256 << 20}, {"panel-before-promotion.json", intent.PreservationHash, 32 << 20}} {
		data, err := privatePromotionFile(dir, file.name, file.limit)
		if err != nil || promotionDigest(data) != file.hash {
			return intent, errors.New("frozen promotion recovery snapshot changed")
		}
	}
	var override map[string]any
	if ok, err := readJSON(filepath.Join(dir, "xui-promotion.json"), &override, false); err != nil || !ok {
		return intent, errors.New("promotion Compose override unavailable")
	}
	actual, _ := json.Marshal(override)
	expected, _ := json.Marshal(promotionOverride(snapshot, panel.Reference))
	if !bytes.Equal(actual, expected) {
		return intent, errors.New("promotion Compose override changed")
	}
	return intent, nil
}

const publishedInspectTemplate = `{"container":` + webInspectTemplate + `,"labels":{{json .Config.Labels}},"host_ports":{{json .HostConfig.PortBindings}},"entrypoint":{{json .Config.Entrypoint}},"command":{{json .Config.Cmd}},"restart":{{json .HostConfig.RestartPolicy.Name}},"privileged":{{json .HostConfig.Privileged}}}`

type publishedInspection struct {
	Container webContainer      `json:"container"`
	Labels    map[string]string `json:"labels"`
	HostPorts map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	} `json:"host_ports"`
	Entrypoint []string `json:"entrypoint"`
	Command    []string `json:"command"`
	Restart    string   `json:"restart"`
	Privileged bool     `json:"privileged"`
}

func (w *installRuntime) inspectPublishedPanel(ctx context.Context, snapshot installRuntimeSnapshot, panel panelIntent, networkID, id string) (webContainer, error) {
	data, err := w.webDocker()(ctx, "inspect", id, "--format", publishedInspectTemplate)
	var inspected publishedInspection
	c := &inspected.Container
	if err != nil || json.Unmarshal(data, &inspected) != nil || c.ID != id || c.Name != "/3x-ui" || c.Project != "xui" || c.Service != "3x-ui" || c.ImageID != panel.ImageID || c.Installation != installationOwner(w.root) || c.Operation != snapshot.Owner || !reflect.DeepEqual(inspected.Entrypoint, []string{"/app/DockerEntrypoint.sh"}) || !reflect.DeepEqual(inspected.Command, []string{"./x-ui"}) || inspected.Restart != "unless-stopped" || inspected.Privileged {
		return *c, errors.New("published panel identity or command does not match the installation")
	}
	if len(c.Networks) > 1 || c.Running && (len(c.Networks) != 1 || c.Networks["proxy"].NetworkID != networkID) {
		return *c, errors.New("published panel network changed")
	}
	for name := range c.Networks {
		if name != "proxy" {
			return *c, errors.New("published panel joined an unexpected network")
		}
	}
	if len(c.Mounts) != 2 {
		return *c, errors.New("published panel mounts changed")
	}
	wantMounts := map[string]struct {
		source string
		rw     bool
	}{"/etc/x-ui": {filepath.Join(w.root, "xui/db"), true}, "/certs": {filepath.Join(w.root, "shared/certs"), false}}
	for _, mount := range c.Mounts {
		destination := strings.TrimRight(mount.Destination, "/")
		want, ok := wantMounts[destination]
		if !ok || mount.Type != "bind" || mount.Source != want.source || mount.RW != want.rw {
			return *c, errors.New("published panel mounts changed")
		}
		delete(wantMounts, destination)
	}
	var doc composeDocument
	if json.Unmarshal(snapshot.Binding.Inputs["xui/compose.json"], &doc) != nil {
		return *c, errors.New("invalid frozen panel routes")
	}
	labels := doc.Services["3x-ui"].Labels
	for key, want := range labels {
		if inspected.Labels[key] != want {
			return *c, errors.New("published panel routes changed")
		}
	}
	for key := range inspected.Labels {
		if strings.HasPrefix(key, "traefik.") {
			if _, ok := labels[key]; !ok {
				return *c, errors.New("unreviewed published panel route appeared")
			}
		}
	}
	if len(inspected.HostPorts) != 2 {
		return *c, errors.New("published panel port declarations changed")
	}
	for _, port := range []string{"443/tcp", "443/udp"} {
		bindings := inspected.HostPorts[port]
		if len(bindings) == 0 {
			return *c, errors.New("published panel port declaration missing")
		}
		for _, binding := range bindings {
			if binding.HostPort != "443" || (binding.HostIP != "" && binding.HostIP != "0.0.0.0" && binding.HostIP != "::") {
				return *c, errors.New("published panel port declarations changed")
			}
		}
	}
	published := map[string]bool{}
	for port, bindings := range c.Ports {
		for _, binding := range bindings {
			if (port != "443/tcp" && port != "443/udp") || binding.HostPort != "443" || (binding.HostIP != "0.0.0.0" && binding.HostIP != "::") {
				return *c, errors.New("published panel has an unexpected host listener")
			}
			published[port] = true
		}
	}
	if c.Running && (!published["443/tcp"] || !published["443/udp"] || c.StartedAt == "") {
		return *c, errors.New("published panel listeners are not ready")
	}
	return *c, nil
}

func (w *installRuntime) promotePanel(ctx context.Context, dir string, snapshot installRuntimeSnapshot) error {
	var c config
	if json.Unmarshal(snapshot.Binding.Inputs[".stack/config.json"], &c) != nil {
		return errors.New("invalid frozen promotion configuration")
	}
	web, err := w.webIntent(ctx, dir, snapshot)
	if err != nil {
		return err
	}
	panel, err := w.panelIntent(ctx, dir, snapshot, web)
	if err != nil {
		return err
	}
	intent, err := w.promotionIntent(ctx, dir, snapshot, web, panel, c)
	if err != nil {
		return err
	}
	observe := func() (map[string]webContainer, error) {
		return w.observeRuntimeServices(ctx, snapshot, web, dir, &panel, &intent)
	}
	found, err := observe()
	if err != nil {
		return err
	}
	ready := w.webReady
	if ready == nil {
		ready = verifyWebCertificates
	}
	if err := ready(ctx, w.root, snapshot.Binding.Inputs); err != nil {
		return err
	}
	bootstrap := found[panelContainerName(snapshot.Owner)]
	if bootstrap.Running {
		_, _ = w.webDocker()(ctx, "stop", "--time", "30", bootstrap.ID)
		found, err = observe()
		if err != nil {
			return err
		}
		if found[panelContainerName(snapshot.Owner)].Running {
			return errors.New("bootstrap panel stop has not completed")
		}
	}
	current := found["3x-ui"]
	if !current.Running {
		if current.ID == "" {
			_, _ = w.webDocker()(ctx, "compose", "--env-file", filepath.Join(dir, "compose.env"), "--project-directory", filepath.Join(w.root, "xui"), "-f", filepath.Join(w.root, "xui/compose.json"), "-f", filepath.Join(dir, "xui-promotion.json"), "up", "-d", "--no-build", "--no-deps", "--no-recreate", "--pull", "never", "3x-ui")
		} else {
			_, _ = w.webDocker()(ctx, "start", current.ID)
		}
		found, err = observe()
		if err != nil {
			return err
		}
		after := found["3x-ui"]
		if !after.Running || after.ID == "" || current.ID != "" && current.ID != after.ID {
			return errors.New("published panel startup has not completed")
		}
		current = after
	}
	if err := deployment.WriteJSON(filepath.Join(dir, "panel-published.json"), webIdentity{current.ID, current.ImageID, current.StartedAt}); err != nil {
		return err
	}
	if err := ready(ctx, w.root, snapshot.Binding.Inputs); err != nil {
		return err
	}
	open := w.publishedPanel
	if open == nil {
		open = managedConfigurationPanel
	}
	p, err := open(c)
	if err != nil {
		return err
	}
	defer p.transport.CloseIdleConnections()
	var s *panelSession
	if w.publishedPanel == nil {
		s, err = waitPromotedPanel(ctx, p, c)
	} else {
		s, err = p.login(ctx, c.Env["PANEL_USER"], c.Env["PANEL_PASS"])
	}
	if err != nil {
		return err
	}
	if err := panelCoreReady(ctx, s); err != nil {
		return err
	}
	preserved, err := s.preservation(ctx)
	if err != nil {
		return err
	}
	if promotionDigest(preserved) != intent.PreservationHash {
		return errors.New("published panel changed identities, subscriptions or settings")
	}
	_, err = observe()
	return err
}

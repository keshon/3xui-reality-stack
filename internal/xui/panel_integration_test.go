//go:build integration

package xui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

const integrationPanelImage = "ghcr.io/mhsanaei/3x-ui:v3.7.0"

// This opt-in test owns only randomly named containers and temporary bind
// mounts. It never discovers a deployed stack or uses Compose/host networking.
func TestPinnedPanelBackupRestore(t *testing.T) {
	if os.Getenv("STACK_TEST_CONTAINERS") != "1" {
		t.Skip("set STACK_TEST_CONTAINERS=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("container integration requires a local Linux Docker daemon")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatal("sqlite3 is required for independent backup integrity checks")
	}
	docker := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
		var out boundedOutput
		cmd.Stdout, cmd.Stderr = &out, io.Discard
		err := cmd.Run()
		return out.Bytes(), err
	}
	if _, err := docker("image", "inspect", integrationPanelImage); err != nil {
		t.Fatal("pull the pinned panel image explicitly before running integration tests")
	}
	create := func(image string, extra []string, command ...string) string {
		t.Helper()
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			t.Fatal("cannot generate isolated container identity")
		}
		owner := hex.EncodeToString(token[:])
		name := "stackctl-integration-" + owner
		// Register before creation so a lost create reply can still be cleaned up.
		t.Cleanup(func() {
			label, err := docker("inspect", "--type", "container", "--format", `{{index .Config.Labels "stackctl.integration"}}`, name)
			if err != nil {
				return // Creation may have failed before allocating a container.
			}
			if strings.TrimSpace(string(label)) != owner {
				t.Error("refusing to remove a container without this test's ownership label")
				return
			}
			if artifacts := os.Getenv("STACK_TEST_ARTIFACT_DIR"); t.Failed() && filepath.IsAbs(artifacts) {
				if err := os.MkdirAll(artifacts, 0700); err != nil {
					t.Error("cannot create private failure artifact directory")
				} else {
					logs, _ := docker("logs", "--tail", "40", name)
					state, _ := docker("inspect", "--format", `{{json .State}}`, name)
					if err := os.WriteFile(filepath.Join(artifacts, name+".log"), append(state, logs...), 0600); err != nil {
						t.Error("cannot save private isolated-container diagnostics")
					}
				}
			}
			if _, err := docker("rm", "--force", "--volumes", name); err != nil {
				t.Errorf("cannot clean up integration container %s", name)
			}
		})
		args := []string{"create", "--pull", "never", "--name", name, "--label", "stackctl.integration=" + owner,
			// The image runs as root; DAC_OVERRIDE permits access to private bind
			// directories owned by the non-root CI runner without making them public.
			"--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--memory", "512m", "--pids-limit", "128"}
		args = append(args, extra...)
		args = append(args, image)
		id, err := docker(append(args, command...)...)
		if err != nil || !dockerID(strings.TrimSpace(string(id))) {
			t.Fatal("cannot create isolated integration container")
		}
		return strings.TrimSpace(string(id))
	}
	createPanel := func(db string, publish bool, command ...string) string {
		args := []string{"--mount", "type=bind,src=" + db + ",dst=/etc/x-ui", "--entrypoint", "/app/x-ui"}
		if publish {
			args = append(args, "--publish", "127.0.0.1::2053/tcp")
		} else {
			args = append(args, "--network", "none")
		}
		return create(integrationPanelImage, args, command...)
	}
	panelEndpoint := func(id, path string) *Panel {
		t.Helper()
		data, err := docker("inspect", "--format", `{{json (index .NetworkSettings.Ports "2053/tcp")}}`, id)
		var ports []struct{ HostIP, HostPort string }
		if err != nil || json.Unmarshal(data, &ports) != nil || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || strings.Trim(ports[0].HostPort, "0123456789") != "" || ports[0].HostPort == "" {
			t.Fatal("isolated panel does not have its expected loopback listener")
		}
		panel, err := NewPanel("http://127.0.0.1:" + ports[0].HostPort + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(panel.transport.CloseIdleConnections)
		return panel
	}
	startPanel := func(db string, basePath ...string) (*Panel, string) {
		t.Helper()
		id := createPanel(db, true, "run")
		if _, err := docker("start", id); err != nil {
			t.Fatal("cannot start isolated panel")
		}
		path := "/integration-panel"
		if len(basePath) > 0 {
			path = basePath[0]
		}
		return panelEndpoint(id, path), id
	}
	waitLogin := func(panel *Panel, password string) *panelSession {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for {
			session, err := panel.login(ctx, "private-admin", password)
			if err == nil {
				return session
			}
			select {
			case <-ctx.Done():
				t.Fatal("isolated panel did not become ready for authentication")
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	ctx := context.Background()
	root := passwordFixture(t)
	source := filepath.Join(root, "xui", "db")
	setup := createPanel(source, false, "setting", "-port", "2053", "-webBasePath", "/bootstrap-panel/")
	if _, err := docker("start", "--attach", setup); err != nil {
		t.Fatal("cannot initialize isolated panel database")
	}
	// docker start's exit code is not the application's exit code.
	exit, err := docker("inspect", "--format", "{{.State.ExitCode}}", setup)
	if err != nil || strings.TrimSpace(string(exit)) != "0" {
		t.Fatal("isolated panel initialization failed")
	}
	initialPanel, panelID := startPanel(source, "/bootstrap-panel")
	bootstrap, err := newPanelBootstrap(config{Env: map[string]string{
		"PANEL_USER": "private-admin", "PANEL_PASS": secret,
		"PANEL_PATH": "/integration-panel", "SUB_PATH": "/integration-sub", "JSON_PATH": "/integration-json",
		"PUBLIC_DOMAIN": "integration.example.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapCtx, cancelBootstrap := context.WithTimeout(ctx, 60*time.Second)
	defer cancelBootstrap()
	for {
		if err := bootstrap.prepare(bootstrapCtx, initialPanel); err == nil {
			break
		}
		select {
		case <-bootstrapCtx.Done():
			t.Fatal("isolated panel bootstrap did not complete through the pinned API")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if _, err := docker("restart", panelID); err != nil {
		t.Fatal("cannot restart the isolated panel after private bootstrap")
	}
	// Docker may allocate a new ephemeral host port after restart. Rediscover
	// the mapping rather than treating the old endpoint as a readiness failure.
	panel := panelEndpoint(panelID, "/integration-panel")
	session := waitLogin(panel, secret)
	if err := bootstrap.verify(ctx, panel); err != nil {
		t.Fatal("isolated panel bootstrap failed post-restart verification")
	}
	var inboundIDs []int
	for n, protocol := range []string{"vless", "trojan"} {
		var created struct {
			ID int `json:"id"`
		}
		payload := map[string]any{
			"remark": "integration-" + protocol, "enable": false,
			"listen": "127.0.0.1", "port": 24001 + n, "protocol": protocol,
			"settings":       `{"clients":[],"decryption":"none","fallbacks":[]}`,
			"streamSettings": `{"network":"tcp","security":"none"}`,
			"sniffing":       `{"enabled":false}`, "trafficReset": "never",
		}
		if err := session.json(ctx, "/panel/api/inbounds/add", payload, &created); err != nil || created.ID <= 0 {
			t.Fatal("cannot seed disabled test inbound through the pinned API")
		}
		inboundIDs = append(inboundIDs, created.ID)
	}
	for _, client := range []map[string]any{
		{"email": "integration-alice", "id": "59b7eca7-cc19-44e0-9fb5-167cc5277e08", "password": "test-trojan-alice", "subId": "test-subscription-alice", "enable": true},
		{"email": "integration-bob", "id": "79b7eca7-cc19-44e0-9fb5-167cc5277e08", "password": "test-trojan-bob", "subId": "test-subscription-bob", "enable": true},
	} {
		if err := session.json(ctx, "/panel/api/clients/add", map[string]any{"client": client, "inboundIds": inboundIDs}, nil); err != nil {
			t.Fatal("cannot seed test client through the pinned API")
		}
	}
	preserved, err := session.preservation(ctx)
	if err != nil || !bytes.Contains(preserved, []byte("integration-alice")) || !bytes.Contains(preserved, []byte("test-subscription-bob")) {
		t.Fatal("seeded clients are absent from the preservation export")
	}
	var exports []struct {
		InboundIDs []int `json:"inboundIds"`
	}
	if err := session.json(ctx, "/panel/api/clients/export", nil, &exports); err != nil || len(exports) != 2 {
		t.Fatal("client export omitted seeded accounts")
	}
	for _, row := range exports {
		if len(row.InboundIDs) != 2 || !slices.Contains(row.InboundIDs, inboundIDs[0]) || !slices.Contains(row.InboundIDs, inboundIDs[1]) {
			t.Fatal("client export omitted inbound attachments")
		}
	}
	backup, err := session.database(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	path := filepath.Join(restored, "x-ui.db")
	if err := os.WriteFile(path, backup, 0600); err != nil {
		t.Fatal("cannot write isolated recovery database")
	}
	checkSQLite := func() {
		t.Helper()
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		check := exec.CommandContext(checkCtx, "sqlite3", "-readonly", path, "PRAGMA integrity_check;")
		check.Stderr = io.Discard
		output, err := check.Output()
		if err != nil || strings.TrimSpace(string(output)) != "ok" {
			t.Fatal("downloaded SQLite backup failed its integrity check")
		}
	}
	checkSQLite()
	// Exercise the engine through a real Traefik management route. The fixture
	// deliberately uses loopback HTTP; production TLS is a separate test gate.
	const traefikImage = "traefik:v3.7.12"
	if _, err := docker("image", "inspect", traefikImage); err != nil {
		t.Fatal("pull the pinned Traefik image explicitly before running integration tests")
	}
	ipBytes, err := docker("inspect", "--format", `{{json .NetworkSettings.Networks}}`, panelID)
	var networks map[string]struct{ IPAddress string }
	if err != nil || json.Unmarshal(ipBytes, &networks) != nil {
		t.Fatal("cannot discover isolated panel network address")
	}
	ip := networks["bridge"].IPAddress
	if net.ParseIP(ip) == nil {
		t.Fatal("isolated panel has no bridge address")
	}
	routes := t.TempDir()
	routesConfig := `{"http":{"routers":{"panel":{"entryPoints":["management"],"rule":"PathPrefix(\u0060/integration-panel/\u0060)","service":"panel"}},"services":{"panel":{"loadBalancer":{"servers":[{"url":"http://` + ip + `:2053"}]}}}}}`
	if err := os.WriteFile(filepath.Join(routes, "routes.yml"), []byte(routesConfig), 0644); err != nil {
		t.Fatal("cannot prepare isolated Traefik routing")
	}
	traefikID := create(traefikImage, []string{"--publish", "127.0.0.1::8443/tcp", "--mount", "type=bind,src=" + routes + ",dst=/test-routes,readonly"}, "--entrypoints.management.address=:8443", "--providers.file.filename=/test-routes/routes.yml", "--log.level=ERROR")
	if _, err := docker("start", traefikID); err != nil {
		t.Fatal("cannot start isolated Traefik")
	}
	portBytes, err := docker("inspect", "--format", `{{(index (index .NetworkSettings.Ports "8443/tcp") 0).HostPort}}`, traefikID)
	port := strings.TrimSpace(string(portBytes))
	if err != nil || port == "" || strings.Trim(port, "0123456789") != "" {
		t.Fatal("cannot discover isolated management port")
	}
	management, err := NewPanel("http://127.0.0.1:" + port + "/integration-panel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(management.transport.CloseIdleConnections)
	waitLogin(management, secret)
	var desired config
	var applied baseline
	if ok, err := readJSON(filepath.Join(root, ".stack", "config.json"), &desired, true); !ok || err != nil {
		t.Fatal("cannot load synthetic managed desired state")
	}
	if ok, err := readJSON(filepath.Join(root, ".stack", "applied.json"), &applied, true); !ok || err != nil {
		t.Fatal("cannot load synthetic managed baseline")
	}
	desired.Env["MANAGEMENT_PORT"], applied.Config.Env["MANAGEMENT_PORT"] = port, port
	writeJSON(t, root, "config.json", desired)
	writeJSON(t, root, "applied.json", applied)
	stack := Stack{docker: func(ctx context.Context, args ...string) ([]byte, error) {
		// Resolve only the two fixed production discovery names to this test's
		// immutable IDs. All inspection and version probes still use real Docker.
		args = append([]string(nil), args...)
		if args[0] == "inspect" {
			switch args[len(args)-1] {
			case "3x-ui":
				args[len(args)-1] = panelID
			case "traefik":
				args[len(args)-1] = traefikID
			default:
				return nil, errors.New("unowned integration inspection target")
			}
		}
		return docker(args...)
	}}
	adapter := passwordExecutionFixture{Stack: stack, panel: management, interrupt: "panel-password"}
	engine := deployment.Engine{Root: root}
	review, err := engine.Review(ctx, adapter)
	if err != nil || !review.CanApply {
		t.Fatal("cannot review the container password workflow")
	}
	status, err := engine.ApplyReview(ctx, review.ID, true, adapter)
	if err == nil || status.Status != "failed" || status.CurrentStep != "panel-password" {
		t.Fatal("injected interruption did not leave a recoverable operation")
	}
	// The remote password has changed, but local credentials must still be old.
	waitLogin(management, newPassword)
	local, _ := os.ReadFile(filepath.Join(root, ".env"))
	if !bytes.Contains(local, []byte(secret)) {
		t.Fatal("interrupted mutation committed local credentials early")
	}
	// Validate the engine's recovery artifact independently, rather than relying
	// only on the earlier API-level backup check.
	engineBackup := filepath.Join(root, ".stack", "go-operations", status.ID, "panel.db")
	backup, err = os.ReadFile(engineBackup)
	if err != nil {
		t.Fatal("engine did not save its recovery database")
	}
	if err := os.WriteFile(path, backup, 0600); err != nil {
		t.Fatal("cannot stage engine backup for restoration")
	}
	checkSQLite()
	adapter.interrupt = "commit"
	status, err = engine.ResumeReviewed(ctx, status.ID, adapter)
	if err == nil || status.Status != "failed" || status.CurrentStep != "commit" {
		t.Fatal("partial commit did not leave a recoverable operation")
	}
	var pending baseline
	if ok, err := readJSON(filepath.Join(root, ".stack", "applied.json"), &pending, true); !ok || err != nil || pending.Config.Env["PANEL_PASS"] != secret {
		t.Fatal("partial commit unexpectedly changed the applied baseline")
	}
	local, _ = os.ReadFile(filepath.Join(root, ".env"))
	if bytes.Contains(local, []byte(secret)) {
		t.Fatal("partial commit did not write its first local output")
	}
	adapter.interrupt = ""
	status, err = engine.ResumeReviewed(ctx, status.ID, adapter)
	if err != nil || status.Status != "complete" {
		t.Fatal("container workflow did not recover from partial commit")
	}
	receipt, err := engine.ApplyReview(ctx, review.ID, true, adapter)
	if err != nil || receipt.ID != status.ID || receipt.Status != "complete" {
		t.Fatal("duplicate apply did not return the completed receipt")
	}
	inspection, err := Inspect(root)
	if err != nil || inspection.Plan().Status != "unchanged" {
		t.Fatal("container workflow did not commit the local baseline")
	}
	rotated := waitLogin(panel, newPassword)
	after, err := rotated.preservation(ctx)
	if err != nil || !bytes.Equal(after, preserved) {
		t.Fatal("password rotation changed preserved client/settings state")
	}
	if _, err := panel.login(ctx, "private-admin", secret); !errors.Is(err, errCredentials) {
		t.Fatal("old credentials were not rejected after rotation")
	}
	// Restore into a second instance, never overwriting the running database.
	recoveryPanel, _ := startPanel(restored)
	recovery := waitLogin(recoveryPanel, secret)
	after, err = recovery.preservation(ctx)
	if err != nil || !bytes.Equal(after, preserved) {
		t.Fatal("restored backup differs from the preserved client/settings state")
	}
	if _, err := recoveryPanel.login(ctx, "private-admin", newPassword); !errors.Is(err, errCredentials) {
		t.Fatal("restored backup unexpectedly accepted the later password")
	}
}

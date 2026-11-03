package xui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type panelDockerFixture struct {
	*webDockerFixture
	panel                     bootstrapInspection
	creates, starts, restarts int
	failAfter                 string
	failInspect               bool
	restartAPI                func() *Panel
	credentials, settings     *int
	configuration             config
}

func (f *panelDockerFixture) run(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "image" && strings.Contains(args[2], "3x-ui") {
		return json.Marshal(map[string]any{"id": "sha256:" + strings.Repeat("8", 64), "digests": []string{"ghcr.io/mhsanaei/3x-ui@sha256:" + strings.Repeat("7", 64)}})
	}
	if args[0] == "inspect" && args[1] == f.panel.Container.ID && f.panel.Container.ID != "" {
		if f.failInspect {
			f.failInspect = false
			return nil, errors.New("synthetic observation interruption")
		}
		return json.Marshal(f.panel)
	}
	if args[0] == "create" {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, f.configuration.Env["PANEL_PASS"]) || strings.Contains(joined, f.configuration.Env["PANEL_PATH"]) || strings.Contains(joined, "443:") {
			f.t.Fatal("bootstrap exposed credentials, paths or public transport ports")
		}
		if f.panel.Container.ID != "" {
			f.t.Fatal("bootstrap recreated an existing panel")
		}
		f.creates++
		p := bootstrapInspection{Traefik: "false", NetworkMode: "bridge", Entrypoint: []string{"/app/x-ui"}, Command: []string{"run"}}
		p.Container.ID = strings.Repeat("6", 64)
		p.Container.ImageID = "sha256:" + strings.Repeat("8", 64)
		for i := range args {
			if args[i] == "--name" {
				p.Container.Name = "/" + args[i+1]
			}
			if args[i] == "--label" {
				key, value, _ := strings.Cut(args[i+1], "=")
				if key == "io.stackctl.installation" {
					p.Container.Installation = value
				}
				if key == "io.stackctl.operation" {
					p.Container.Operation = value
				}
			}
		}
		p.Container.Mounts = append(p.Container.Mounts, struct {
			Type, Source, Destination string
			RW                        bool
		}{"bind", filepath.Join(f.root, "xui/db"), "/etc/x-ui", true}, struct {
			Type, Source, Destination string
			RW                        bool
		}{"bind", filepath.Join(f.root, "shared/certs"), "/certs", false})
		_ = json.Unmarshal([]byte(`{"2053/tcp":[{"HostIp":"127.0.0.1","HostPort":""}]}`), &p.HostPorts)
		f.panel = p
		f.savePanel()
		if f.failAfter == "create" {
			f.failInspect = true
		}
		return nil, errors.New("synthetic lost create reply")
	}
	if args[0] == "start" || args[0] == "restart" {
		if args[1] != f.panel.Container.ID {
			f.t.Fatal("panel operation used a mutable container name")
		}
		if args[0] == "start" {
			f.starts++
		} else {
			if *f.credentials != 1 || *f.settings != 1 {
				f.t.Fatal("panel restart preceded verified configuration")
			}
			f.restarts++
			f.restartAPI()
		}
		f.panel.Container.Running = true
		f.panel.Container.StartedAt = "2026-11-03T10:00:00Z"
		if f.restarts != 0 {
			f.panel.Container.StartedAt = "2026-11-03T10:01:00Z"
		}
		f.panel.Container.Networks = map[string]struct{ NetworkID string }{"bridge": {strings.Repeat("5", 64)}}
		_ = json.Unmarshal([]byte(`{"2053/tcp":[{"HostIp":"127.0.0.1","HostPort":"49152"}],"443/tcp":null}`), &f.panel.Container.Ports)
		if err := os.WriteFile(filepath.Join(f.root, "xui/db/x-ui.db"), []byte("synthetic owned database"), 0600); err != nil {
			f.t.Fatal(err)
		}
		f.savePanel()
		if f.failAfter == args[0] {
			f.failInspect = true
		}
		return nil, errors.New("synthetic lost lifecycle reply")
	}
	return f.webDockerFixture.run(ctx, args...)
}

func (f *panelDockerFixture) savePanel() {
	f.containers[strings.TrimPrefix(f.panel.Container.Name, "/")] = f.panel.Container
}

func panelWorkflowFixture(t *testing.T) (*installRuntime, *panelDockerFixture, *runtimeHelperFixture) {
	t.Helper()
	w, web, helper := webWorkflowFixture(t)
	var c config
	if json.Unmarshal(w.binding.Inputs[".stack/config.json"], &c) != nil {
		t.Fatal("invalid test configuration")
	}
	_, initial, restart, credentials, settings := bootstrapFixture(t, "normal", c)
	docker := &panelDockerFixture{webDockerFixture: web, restartAPI: restart, credentials: credentials, settings: settings, configuration: c}
	w.stack.docker = docker.run
	w.panelEndpoint = func(_ webContainer, prefix string) (*Panel, error) {
		return NewPanel(initial.base + strings.TrimRight(prefix, "/"))
	}
	return w, docker, helper
}

func TestPanelStartupRecoversLifecycleInterruptions(t *testing.T) {
	for _, point := range []string{"normal", "create", "start", "restart", "checkpoint", "readiness"} {
		t.Run(point, func(t *testing.T) {
			w, docker, helper := panelWorkflowFixture(t)
			docker.failAfter = point
			if point == "readiness" {
				original := w.panelEndpoint
				pending := true
				w.panelEndpoint = func(c webContainer, prefix string) (*Panel, error) {
					if prefix != "" && pending {
						pending = false
						return nil, errors.New("synthetic panel readiness pending")
					}
					return original(c, prefix)
				}
			}
			engine := deployment.Engine{Root: w.root}
			var workflow deployment.Workflow = w
			if point == "checkpoint" {
				workflow = &runtimeFailure{installRuntime: w, after: "panel"}
			}
			if err := engine.Start(t.Context(), workflow); err == nil {
				t.Fatal("unfinished installation claimed success")
			}
			resumed := &installRuntime{root: w.root, stack: w.stack, helper: helper, webReady: w.webReady, panelEndpoint: w.panelEndpoint}
			if err := engine.Resume(t.Context(), resumed); err == nil || !strings.Contains(err.Error(), "verify") {
				t.Fatalf("panel recovery did not reach final-verification boundary: %v", err)
			}
			if docker.creates != 1 || docker.starts != 1 || docker.restarts != 1 || *docker.credentials != 1 || *docker.settings != 1 || docker.webDockerFixture.starts != 2 {
				t.Fatal("recovery repeated bootstrap effects")
			}
			if _, _, err := readDraft(w.root); err == nil {
				t.Fatal("ordinary initialization adopted a started database")
			}
			if _, err := os.Stat(filepath.Join(w.root, ".stack/applied.json")); !os.IsNotExist(err) {
				t.Fatal("partial installation wrote applied state")
			}
		})
	}
}

func TestPanelRecoveryRejectsResourceAndDatabaseDrift(t *testing.T) {
	for _, fault := range []string{"replaced", "missing", "mount", "public-port", "public-route", "network", "network-id", "entrypoint", "restart", "stopped", "port", "database", "missing-database", "reviewed-file"} {
		t.Run(fault, func(t *testing.T) {
			w, docker, helper := panelWorkflowFixture(t)
			engine := deployment.Engine{Root: w.root}
			if err := engine.Start(t.Context(), &runtimeFailure{installRuntime: w, after: "panel"}); err == nil {
				t.Fatal("expected interrupted checkpoint")
			}
			switch fault {
			case "replaced":
				docker.panel.Container.ID = strings.Repeat("4", 64)
			case "missing":
				delete(docker.containers, strings.TrimPrefix(docker.panel.Container.Name, "/"))
			case "mount":
				docker.panel.Container.Mounts[0].Source = "/foreign/db"
			case "public-port":
				docker.panel.HostPorts["2053/tcp"][0].HostIP = "0.0.0.0"
			case "public-route":
				docker.panel.Traefik = "true"
			case "network":
				docker.panel.Container.Networks["proxy"] = struct{ NetworkID string }{docker.network.ID}
			case "network-id":
				docker.panel.Container.Networks["bridge"] = struct{ NetworkID string }{strings.Repeat("3", 64)}
			case "entrypoint":
				docker.panel.Entrypoint = []string{"/bin/sh"}
			case "restart":
				docker.panel.Container.StartedAt = "2026-11-03T10:02:00Z"
			case "stopped":
				docker.panel.Container.Running = false
			case "port":
				docker.panel.Container.Ports["2053/tcp"][0].HostPort = "49153"
			case "database":
				if err := os.WriteFile(filepath.Join(w.root, "xui/db/foreign.db"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-database":
				if err := os.Remove(filepath.Join(w.root, "xui/db/x-ui.db")); err != nil {
					t.Fatal(err)
				}
			case "reviewed-file":
				if err := os.WriteFile(filepath.Join(w.root, "users.txt"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if fault != "missing" {
				docker.savePanel()
			}
			resumed := &installRuntime{root: w.root, stack: w.stack, helper: helper, webReady: w.webReady, panelEndpoint: w.panelEndpoint}
			if err := engine.Resume(t.Context(), resumed); err == nil || !strings.Contains(err.Error(), "panel") {
				t.Fatalf("resource drift was accepted: %v", err)
			}
			if docker.creates != 1 || docker.starts != 1 || docker.restarts != 1 || *docker.credentials != 1 || *docker.settings != 1 {
				t.Fatal("conflict triggered bootstrap effects")
			}
		})
	}
}

func TestPanelBootstrapRejectsDatabaseBeforeFirstStart(t *testing.T) {
	w, docker, helper := panelWorkflowFixture(t)
	docker.failAfter = "create"
	engine := deployment.Engine{Root: w.root}
	if err := engine.Start(t.Context(), w); err == nil {
		t.Fatal("expected create interruption")
	}
	if docker.starts != 0 {
		t.Fatal("interrupted creation started a panel")
	}
	if err := os.WriteFile(filepath.Join(w.root, "xui/db/x-ui.db"), []byte("foreign database"), 0600); err != nil {
		t.Fatal(err)
	}
	resumed := &installRuntime{root: w.root, stack: w.stack, helper: helper, webReady: w.webReady, panelEndpoint: w.panelEndpoint}
	if err := engine.Resume(t.Context(), resumed); err == nil {
		t.Fatal("foreign database was adopted before first start")
	}
	if docker.starts != 0 || *docker.credentials != 0 || *docker.settings != 0 {
		t.Fatal("foreign database triggered startup or credential writes")
	}
}

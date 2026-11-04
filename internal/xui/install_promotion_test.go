package xui

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

type promotionDockerFixture struct {
	*panelDockerFixture
	final               publishedInspection
	stops, publicStarts int
	failAfter           string
	interrupted         bool
	onPublicStart       func()
}

func (f *promotionDockerFixture) run(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "ps" && f.interrupted {
		f.interrupted = false
		return nil, errors.New("synthetic lost observation")
	}
	if args[0] == "inspect" && args[1] == f.final.Container.ID && f.final.Container.ID != "" {
		return json.Marshal(f.final)
	}
	if args[0] == "stop" {
		if args[len(args)-1] != f.panel.Container.ID {
			f.t.Fatal("stop used an unowned or mutable identity")
		}
		f.stops++
		f.panel.Container.Running = false
		f.panel.Container.Ports = nil
		f.savePanel()
		if f.failAfter == "stop" {
			f.interrupted = true
		}
		return nil, errors.New("synthetic lost stop reply")
	}
	if args[0] == "compose" && args[len(args)-1] == "3x-ui" {
		if f.panel.Container.Running {
			f.t.Fatal("two panels would open the same database")
		}
		if f.final.Container.ID != "" {
			f.t.Fatal("promotion recreated an existing final container")
		}
		if !strings.Contains(strings.Join(args, " "), "--no-recreate --pull never") {
			f.t.Fatal("promotion followed tags or allowed recreation")
		}
		var overlay string
		for i := range args {
			if args[i] == "-f" {
				overlay = args[i+1]
			}
		}
		var override struct {
			Services map[string]struct {
				Image  string
				Labels map[string]string
			}
		}
		if ok, err := readJSON(overlay, &override, false); err != nil || !ok {
			f.t.Fatal("missing promotion override")
		}
		var doc composeDocument
		if ok, err := readJSON(filepath.Join(f.root, "xui/compose.json"), &doc, false); err != nil || !ok {
			f.t.Fatal("missing final Compose")
		}
		service := override.Services["3x-ui"]
		if service.Image != "ghcr.io/mhsanaei/3x-ui@sha256:"+strings.Repeat("7", 64) {
			f.t.Fatal("promotion did not reuse the frozen panel image")
		}
		p := publishedInspection{Entrypoint: []string{"/app/DockerEntrypoint.sh"}, Command: []string{"./x-ui"}, Restart: "unless-stopped", Labels: map[string]string{}}
		for k, v := range doc.Services["3x-ui"].Labels {
			p.Labels[k] = v
		}
		for k, v := range service.Labels {
			p.Labels[k] = v
		}
		p.Container = webContainer{Name: "/3x-ui", Project: "xui", Service: "3x-ui", Installation: service.Labels["io.stackctl.installation"], Operation: service.Labels["io.stackctl.operation"]}
		p.Container.ID = strings.Repeat("4", 64)
		p.Container.ImageID = f.panel.Container.ImageID
		p.Container.Running = true
		p.Container.StartedAt = "2026-11-04T10:00:00Z"
		p.Container.Mounts = f.panel.Container.Mounts
		p.Container.Networks = map[string]struct{ NetworkID string }{"proxy": {f.network.ID}}
		_ = json.Unmarshal([]byte(`{"443/tcp":[{"HostIp":"","HostPort":"443"}],"443/udp":[{"HostIp":"","HostPort":"443"}]}`), &p.HostPorts)
		_ = json.Unmarshal([]byte(`{"443/tcp":[{"HostIp":"0.0.0.0","HostPort":"443"}],"443/udp":[{"HostIp":"0.0.0.0","HostPort":"443"}],"2053/tcp":null}`), &p.Container.Ports)
		f.final = p
		f.saveFinal()
		f.publicStarts++
		f.network.Endpoints[p.Container.ID] = json.RawMessage(`{}`)
		f.network.Containers = len(f.network.Endpoints)
		if f.onPublicStart != nil {
			f.onPublicStart()
		}
		if f.failAfter == "public-start" {
			f.interrupted = true
		}
		return nil, errors.New("synthetic lost final startup reply")
	}
	return f.panelDockerFixture.run(ctx, args...)
}

func (f *promotionDockerFixture) saveFinal() { f.containers["3x-ui"] = f.final.Container }

func promotionWorkflowFixture(t *testing.T) (*installRuntime, *promotionDockerFixture, *provisioningFixture, string) {
	t.Helper()
	w, docker, _ := panelWorkflowFixture(t)
	engine := deployment.Engine{Root: w.root}
	if err := engine.Start(t.Context(), panelBootstrapBoundary{w, false}); err == nil {
		t.Fatal("expected unfinished verification")
	}
	status, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.root, ".stack/go-operations", status.ID)
	c := docker.configuration
	p, err := newProvisioningIntent(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.inbounds(c)
	if err != nil {
		t.Fatal(err)
	}
	api, s := newProvisioningFixture(t)
	for i := range rows {
		rows[i].ID = 10 + i
		p.InboundIDs[rows[i].Remark] = rows[i].ID
	}
	api.catalog.Inbounds = rows
	for _, client := range p.Clients {
		api.catalog.Clients = append(api.catalog.Clients, provisionedAttachment{client, []int{10, 11, 12}})
		p.AppliedClients[client.Email] = true
	}
	b, err := newPanelBootstrap(c)
	if err != nil {
		t.Fatal(err)
	}
	api.settings = b.settings
	api.coreState = "running"
	if err := deployment.WriteJSON(filepath.Join(dir, "provisioning.json"), p); err != nil {
		t.Fatal(err)
	}
	w.panelEndpoint = func(webContainer, string) (*Panel, error) { return NewPanel(s.base) }
	w.publishedPanel = func(config) (*Panel, error) { return NewPanel(s.base) }
	w.subscriptionRead = func(ctx context.Context, c config, subID string) ([]byte, error) {
		for _, client := range p.Clients {
			if client.SubID == subID {
				return nativeSubscriptionBody(t, c, p, client), nil
			}
		}
		return nil, errors.New("unknown test subscription")
	}
	w.jsonRead = func(context.Context, config, string) ([]byte, error) {
		return nil, errors.New("transfer boundary not supplied by this test")
	}
	promotion := &promotionDockerFixture{panelDockerFixture: docker}
	w.stack.docker = promotion.run
	return w, promotion, api, dir
}

func runPromotion(t *testing.T, w *installRuntime, dir string) error {
	t.Helper()
	unlock, err := deployment.Lock(w.root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	snapshot, err := w.open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return w.promotePanel(t.Context(), dir, snapshot)
}

func TestPromotionRecoversStopStartupAndReadinessWithoutRecreating(t *testing.T) {
	for _, point := range []string{"normal", "stop", "public-start", "readiness"} {
		t.Run(point, func(t *testing.T) {
			w, docker, _, dir := promotionWorkflowFixture(t)
			docker.failAfter = point
			if point == "readiness" {
				original := w.publishedPanel
				pending := true
				w.publishedPanel = func(c config) (*Panel, error) {
					if pending {
						pending = false
						return nil, errors.New("TLS routing not yet ready")
					}
					return original(c)
				}
			}
			engine := deployment.Engine{Root: w.root}
			_ = engine.Resume(t.Context(), w)
			if err := engine.Resume(t.Context(), w); err == nil || !strings.Contains(err.Error(), "verify") {
				t.Fatalf("unfinished transfers were marked verified: %v", err)
			}
			if err := runPromotion(t, w, dir); err != nil {
				t.Fatalf("promotion itself did not become ready: %v", err)
			}
			if docker.stops != 1 || docker.publicStarts != 1 || docker.creates != 1 || docker.restarts != 1 {
				t.Fatal("promotion recovery repeated lifecycle effects")
			}
			if docker.panel.Container.Running || !docker.final.Container.Running {
				t.Fatal("promotion did not leave one running panel")
			}
			if _, err := os.Stat(filepath.Join(dir, "promotion-intent.json")); err != nil {
				t.Fatal("promotion recovery intent missing")
			}
			if _, err := os.Stat(filepath.Join(dir, "panel-published.json")); err != nil {
				t.Fatal("published identity missing")
			}
			if _, err := os.Stat(filepath.Join(w.root, ".stack/applied.json")); !os.IsNotExist(err) {
				t.Fatal("promotion bypassed transfer verification")
			}
		})
	}
}

func TestPromotionRefusesChangedSnapshotsAndPublishedResources(t *testing.T) {
	for _, fault := range []string{"backup", "preservation", "override", "id", "removed", "mount", "route", "extra-route", "port", "entrypoint", "privileged", "network", "restart", "bootstrap-running"} {
		t.Run(fault, func(t *testing.T) {
			w, docker, _, dir := promotionWorkflowFixture(t)
			engine := deployment.Engine{Root: w.root}
			_ = engine.Resume(t.Context(), w)
			if docker.publicStarts != 1 {
				t.Fatal("fixture did not reach promotion")
			}
			switch fault {
			case "backup":
				if err := os.WriteFile(filepath.Join(dir, "panel-before-promotion.db"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "preservation":
				if err := os.WriteFile(filepath.Join(dir, "panel-before-promotion.json"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "override":
				if err := os.WriteFile(filepath.Join(dir, "xui-promotion.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "id":
				docker.final.Container.ID = strings.Repeat("3", 64)
			case "removed":
				delete(docker.containers, "3x-ui")
			case "mount":
				docker.final.Container.Mounts = append(docker.final.Container.Mounts[:0:0], docker.final.Container.Mounts...)
				docker.final.Container.Mounts[0].Source = "/foreign/db"
			case "route":
				docker.final.Labels["traefik.enable"] = "false"
			case "extra-route":
				docker.final.Labels["traefik.http.routers.unreviewed.rule"] = "PathPrefix(`/`)"
			case "port":
				docker.final.Container.Ports["443/tcp"][0].HostPort = "444"
			case "entrypoint":
				docker.final.Entrypoint = []string{"/bin/sh"}
			case "privileged":
				docker.final.Privileged = true
			case "network":
				docker.final.Container.Networks = map[string]struct{ NetworkID string }{"bridge": {strings.Repeat("5", 64)}}
			case "restart":
				docker.final.Container.StartedAt = "2026-11-04T10:01:00Z"
			case "bootstrap-running":
				docker.panel.Container.Running = true
				docker.savePanel()
			}
			if fault != "removed" {
				docker.saveFinal()
			}
			if err := runPromotion(t, w, dir); err == nil {
				t.Fatal("promotion accepted changed recovery state")
			}
			if docker.stops != 1 || docker.publicStarts != 1 {
				t.Fatal("changed state triggered another lifecycle action")
			}
		})
	}
}

func TestPromotionWaitsForCertificatesWithoutStoppingBootstrap(t *testing.T) {
	w, docker, _, dir := promotionWorkflowFixture(t)
	pending := true
	w.webReady = func(context.Context, string, map[string][]byte) error {
		if pending {
			return errors.New("certificate not ready")
		}
		return nil
	}
	if err := runPromotion(t, w, dir); err == nil {
		t.Fatal("promotion ignored certificate readiness")
	}
	if docker.stops != 0 || docker.publicStarts != 0 || !docker.panel.Container.Running {
		t.Fatal("pending certificate interrupted the private panel or exposed ports")
	}
	pending = false
	if err := runPromotion(t, w, dir); err != nil {
		t.Fatal(err)
	}
	if docker.stops != 1 || docker.publicStarts != 1 {
		t.Fatal("certificate readiness recovery repeated lifecycle effects")
	}
}

func TestPromotionRejectsCoreFailureAndIdentityChanges(t *testing.T) {
	for _, fault := range []string{"core-before", "core-after", "credentials-after"} {
		t.Run(fault, func(t *testing.T) {
			w, docker, api, dir := promotionWorkflowFixture(t)
			if fault == "core-before" {
				api.coreState = "error"
			} else {
				docker.onPublicStart = func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					if fault == "core-after" {
						api.coreState = "error"
					} else {
						api.catalog.Clients[0].Client.SubID = "unexpected"
					}
				}
			}
			engine := deployment.Engine{Root: w.root}
			if err := engine.Resume(t.Context(), w); err == nil {
				t.Fatal("unhealthy or changed deployment was accepted")
			}
			err := runPromotion(t, w, dir)
			if err == nil || fault == "credentials-after" && !strings.Contains(err.Error(), "identities") || fault != "credentials-after" && !strings.Contains(err.Error(), "core") {
				t.Fatalf("promotion did not reject the failed acceptance check: %v", err)
			}
			if fault == "core-before" && (docker.stops != 0 || docker.publicStarts != 0) {
				t.Fatal("core failure did not block exposure")
			}
			if _, err := os.Stat(filepath.Join(w.root, ".stack/applied.json")); !os.IsNotExist(err) {
				t.Fatal("failed verification committed applied state")
			}
		})
	}
}

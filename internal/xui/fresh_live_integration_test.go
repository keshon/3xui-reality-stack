//go:build integration

package xui

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
	"github.com/keshon/3xui-reality-stack/internal/host"
)

// Acceptance-only entry point for the private installer. It never bypasses host
// inventory, ownership, certificate or transfer gates and never removes resources.
// The operator supplies an empty Docker environment and a separately initialized
// draft. Each phase can run in a new process, exercising saved recovery state.
func TestLiveFreshInstallation(t *testing.T) {
	if os.Getenv("STACK_TEST_FRESH_INSTALL") != "1" {
		t.Skip("requires explicit fresh-host acceptance opt-in")
	}
	root, binary, phase := os.Getenv("STACK_TEST_FRESH_ROOT"), os.Getenv("STACK_TEST_CONTROLLER"), os.Getenv("STACK_TEST_FRESH_PHASE")
	if runtime.GOOS != "linux" || !filepath.IsAbs(root) || root == "/" || !filepath.IsAbs(binary) {
		t.Fatal("fresh acceptance requires absolute Linux draft/controller paths")
	}
	if phase != "start" && phase != "commit" && phase != "finish" && phase != "running" {
		t.Fatal("phase must be start, commit, finish or running")
	}
	ctx := t.Context()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		t.Fatal("release controller version unavailable")
	}
	s := Stack{}
	// The test executable owns the workflow; the checksum-verified release owns
	// the real systemd helper. No simulated host/service observations are used.
	s.installHost = func(ctx context.Context, root, domain string) (installHostObservation, error) {
		controller, err := installedController(ctx, binary, strings.TrimSpace(string(version)))
		if err != nil {
			return installHostObservation{}, err
		}
		service, err := certificateService(root, controller.Path, domain)
		if err != nil {
			return installHostObservation{}, err
		}
		state, err := host.ObserveService(ctx, service.Name)
		return installHostObservation{Controller: controller, Service: service, ServiceState: state}, err
	}
	w := &installRuntime{root: root, stack: s}
	engine := deployment.Engine{Root: root}
	if phase == "running" {
		verifyRunningInstallation(t, w)
		return
	}
	interrupted := false
	var stageError error
	if phase == "start" {
		status, statusErr := engine.Status()
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		boundary := acceptanceBoundary{w, "services", &interrupted, &stageError}
		if status.Status == "idle" {
			spec, specErr := s.reviewInstall(ctx, root)
			if specErr != nil {
				t.Fatal(specErr)
			}
			binding, bindingErr := decodeInstallBinding(spec.Binding)
			if bindingErr != nil {
				t.Fatal(bindingErr)
			}
			w.binding = &binding
			err = engine.Start(ctx, boundary)
		} else {
			err = engine.Resume(ctx, boundary)
		}
		if err == nil || !interrupted {
			t.Fatalf("start failed before the intended interruption: %v; stage: %v", err, stageError)
		}
	} else if phase == "commit" {
		err = engine.Resume(ctx, acceptanceBoundary{w, "commit", &interrupted, &stageError})
		if err == nil || !interrupted {
			t.Fatalf("resume failed before the intended commit interruption: %v; stage: %v", err, stageError)
		}
	} else {
		before, err := os.ReadFile(filepath.Join(root, ".stack/applied.json"))
		if err != nil {
			t.Fatal("verified applied baseline missing before recovery")
		}
		if err := engine.Resume(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := engine.Resume(ctx, &installRuntime{root: root, stack: s}); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(filepath.Join(root, ".stack/applied.json"))
		if err != nil || string(before) != string(after) {
			t.Fatal("commit replay changed the applied baseline")
		}
		inspection, err := s.Inspect(ctx, root)
		if err != nil || inspection.Plan.Status != "unchanged" {
			t.Fatal("completed installation is not managed and unchanged")
		}
	}
	status, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if phase == "finish" && status.Status != "complete" {
		t.Fatal("recovered operation is incomplete")
	}
	data, _ := json.Marshal(status)
	t.Logf("phase=%s operation=%s", phase, data)
}

// Post-restart checks retain IDs/configuration ownership but intentionally do
// not require the install-time StartedAt timestamps after a completed operation.
func verifyRunningInstallation(t *testing.T, w *installRuntime) {
	t.Helper()
	ctx := t.Context()
	status, err := (deployment.Engine{Root: w.root}).Status()
	if err != nil || status.Status != "complete" || status.Kind != "xui-install-v1" {
		t.Fatal("running acceptance requires a completed installation")
	}
	dir := filepath.Join(w.root, ".stack/go-operations", status.ID)
	var snapshot installRuntimeSnapshot
	var panel panelIntent
	var published webIdentity
	var promotion promotionIntent
	var plan provisioningIntent
	for name, target := range map[string]any{"installation.json": &snapshot, "panel-intent.json": &panel, "panel-published.json": &published, "promotion-intent.json": &promotion, "provisioning.json": &plan} {
		if exists, err := readJSON(filepath.Join(dir, name), target, true); err != nil || !exists {
			t.Fatal("running acceptance recovery evidence missing")
		}
	}
	if snapshot.Root != w.root || snapshot.Owner != status.ID {
		t.Fatal("running acceptance root/operation ownership mismatch")
	}
	network, err := networkReceipt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.inspectPublishedPanel(ctx, snapshot, panel, network, published.ID); err != nil {
		t.Fatal(err)
	}
	runtimeReport, err := w.stack.Runtime(ctx, w.root)
	if err != nil || runtimeReport.Status != "compatible" {
		t.Fatal("running runtime is incompatible")
	}
	var applied baseline
	if exists, err := readJSON(filepath.Join(w.root, ".stack/applied.json"), &applied, false); err != nil || !exists {
		t.Fatal("running baseline missing")
	}
	c := applied.Config
	p, err := managedConfigurationPanel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.transport.CloseIdleConnections()
	s, err := waitPromotedPanel(ctx, p, c)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := s.preservation(ctx)
	if err != nil || promotionDigest(preserved) != promotion.PreservationHash {
		t.Fatal("restart changed client identities or settings")
	}
	if err := verifyFreshRouting(ctx, s); err != nil {
		t.Fatal(err)
	}
	for i, client := range plan.Clients {
		native, err := readNativeSubscription(ctx, c, client.SubID)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateNativeSubscription(native, c, &plan, client); err != nil {
			t.Fatal(err)
		}
		jsonConfig := c
		jsonConfig.Env = maps.Clone(c.Env)
		jsonConfig.Env["SUB_PATH"] = c.Env["JSON_PATH"]
		body, err := readNativeSubscription(ctx, jsonConfig, client.SubID)
		if err != nil {
			t.Fatal(err)
		}
		outbounds, err := checkedJSONOutbounds(body, c, &plan, client)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for _, outbound := range outbounds {
				input, _ := json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": 39080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []json.RawMessage{outbound}})
				if err := probePublishedTransfer(ctx, published.ID, runtime.GOARCH, input); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	t.Log("running installation: ownership, runtime, preserved identities/settings, routing, native/JSON subscriptions and all three authenticated transfers passed")
}

type acceptanceBoundary struct {
	*installRuntime
	after       string
	interrupted *bool
	stageError  *error
}

func (w acceptanceBoundary) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	steps, err := w.installRuntime.Steps(ctx, dir)
	for i := range steps {
		id, run := steps[i].ID, steps[i].Run
		steps[i].Run = func(ctx context.Context) error {
			if err := run(ctx); err != nil {
				*w.stageError = err
				return err
			}
			if id == w.after {
				*w.interrupted = true
				return errors.New("acceptance checkpoint interruption after effects")
			}
			return nil
		}
	}
	return steps, err
}

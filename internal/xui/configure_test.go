package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keshon/3xui-reality-stack/internal/control"
	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestConfigurePasswordChangesOnlyDesiredState(t *testing.T) {
	root, _, _ := fixture(t)
	stack := PasswordStack{Stack: Stack{docker: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("configuration editor invoked Docker")
		return nil, nil
	}}}
	before := files(t, root)
	plan, err := stack.Configure(context.Background(), root, map[string]string{"PANEL_PASS": newPassword})
	if err != nil || plan.Status != "proposed" {
		t.Fatal("desired password edit did not produce a plan")
	}
	for path, original := range before {
		if path == filepath.Join(root, ".stack", "config.json") {
			continue
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Fatal("configuration edit changed generated or applied state")
		}
	}
	var desired config
	if ok, err := readJSON(filepath.Join(root, ".stack", "config.json"), &desired, true); !ok || err != nil || desired.Env["PANEL_PASS"] != newPassword {
		t.Fatal("password literal did not survive configuration save")
	}
	public, _ := json.Marshal(plan)
	if bytes.Contains(public, []byte(secret)) || bytes.Contains(public, []byte("пароль")) {
		t.Fatal("configuration output exposed a password")
	}
	saved, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
	if _, err := stack.Configure(context.Background(), root, map[string]string{"PANEL_PASS": newPassword}); err != nil {
		t.Fatal(err)
	}
	repeated, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
	if !bytes.Equal(saved, repeated) {
		t.Fatal("idempotent password edit rewrote desired configuration")
	}
	// Reverting an unapplied edit only changes desired state, with no operation.
	plan, err = stack.Configure(context.Background(), root, map[string]string{"PANEL_PASS": secret})
	if err != nil || plan.Status != "unchanged" {
		t.Fatal("pending password edit could not be reverted")
	}
	status, err := (deployment.Engine{Root: root}).Status()
	if err != nil || status.Status != "idle" {
		t.Fatal("configuration edit registered a deployment operation")
	}
}

func TestConfigurePasswordRefusesUnsafeState(t *testing.T) {
	for _, scenario := range []string{"generated-drift", "unrelated-edit", "node", "legacy-operation", "legacy-adoption", "legacy-upgrade", "go-operation", "malformed-go", "cancelled-context", "held-lock", "config-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, desired, _ := fixture(t)
			ctx := context.Background()
			switch scenario {
			case "generated-drift":
				os.WriteFile(filepath.Join(root, ".env"), []byte("manual-edit"), 0600)
			case "unrelated-edit":
				desired.Users = append(desired.Users, "another-user")
				writeJSON(t, root, "config.json", desired)
			case "node":
				desired.Env["ROLE"] = "node"
				writeJSON(t, root, "config.json", desired)
				var previous baseline
				readJSON(filepath.Join(root, ".stack", "applied.json"), &previous, true)
				previous.Config.Env["ROLE"] = "node"
				writeJSON(t, root, "applied.json", previous)
			case "legacy-operation", "legacy-adoption", "legacy-upgrade":
				name := strings.TrimPrefix(scenario, "legacy-") + ".json"
				writeJSON(t, root, name, map[string]string{"status": "running"})
			case "go-operation":
				writeJSON(t, root, "go-operation.json", map[string]any{"version": 1, "id": strings.Repeat("a", 32), "kind": "xui-password-v1", "status": "preparing", "steps": []string{}, "next": 0})
			case "malformed-go":
				writeJSON(t, root, "go-operation.json", map[string]any{"version": 1, "status": "complete"})
			case "cancelled-context":
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			case "held-lock":
				unlock, err := deployment.Lock(root)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			case "config-symlink":
				target := filepath.Join(root, ".stack", "config-target.json")
				path := filepath.Join(root, ".stack", "config.json")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("symlink creation unavailable")
				}
			}
			before, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
			if _, err := (PasswordStack{}).Configure(ctx, root, map[string]string{"PANEL_PASS": newPassword}); err == nil {
				t.Fatal("configuration edit accepted unsafe state")
			}
			after, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("rejected edit changed desired configuration")
			}
		})
	}
}

func TestRemoteConfigurationUsesFixedRootAndSanitizesErrors(t *testing.T) {
	root, _, _ := fixture(t)
	controller := control.Controller{Root: root, Stack: PasswordStack{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	left, right := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); _ = control.Serve(ctx, right, right, controller) }()
	client, err := control.Connect(ctx, left)
	if err != nil {
		left.Close()
		t.Fatal(err)
	}
	defer func() { client.Close(); <-done }()
	plan, err := client.Configure(ctx, map[string]string{"PANEL_PASS": newPassword})
	if err != nil || plan.Status != "proposed" {
		t.Fatal("remote configuration did not save desired password")
	}
	encoded, _ := json.Marshal(plan)
	if bytes.Contains(encoded, []byte("пароль")) || bytes.Contains(encoded, []byte(root)) {
		t.Fatal("remote configuration exposed private input or path")
	}
	if _, err := client.Configure(ctx, map[string]string{"private-field": "private-value"}); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("remote configuration accepted or echoed unsupported input")
	}
	var desired config
	if ok, err := readJSON(filepath.Join(root, ".stack", "config.json"), &desired, true); !ok || err != nil || desired.Env["PANEL_PASS"] != newPassword {
		t.Fatal("remote editor changed the wrong configuration")
	}
	if _, err := (control.Controller{Root: root, Stack: Stack{}}).Configure(ctx, map[string]string{"PANEL_PASS": newPassword}); !errors.Is(err, deployment.ErrExecutionUnavailable) {
		t.Fatal("default controller exposed configuration writes")
	}
}

func TestEditingDesiredPasswordInvalidatesExistingReview(t *testing.T) {
	root := passwordFixture(t)
	adapter := passwordExecutionFixture{Stack: passwordTestStack(root)}
	engine := deployment.Engine{Root: root}
	ctx := context.Background()
	review, err := engine.Review(ctx, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (PasswordStack{}).Configure(ctx, root, map[string]string{"PANEL_PASS": "another-private-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyReview(ctx, review.ID, true, adapter); !errors.Is(err, deployment.ErrStaleReview) {
		t.Fatal("old review remained executable after desired edit")
	}
	if status, err := engine.Status(); err != nil || status.Status != "idle" {
		t.Fatal("stale review started an operation")
	}
}

func TestConfigureRejectsInvalidFieldsAndSecrets(t *testing.T) {
	for _, changes := range []map[string]string{nil, {"OTHER_SECRET": "private-value"}, {"PANEL_PASS": "new", "AWG_PORT": "9999"}, {"PANEL_PASS": ""}, {"PANEL_PASS": "private\nvalue"}, {"PANEL_PASS": string([]byte{0xff})}, {"PANEL_PASS": strings.Repeat("x", 4097)}} {
		root, _, _ := fixture(t)
		before, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
		_, err := (PasswordStack{}).Configure(context.Background(), root, changes)
		if err == nil || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "OTHER_SECRET") {
			t.Fatal("invalid edit accepted or secret-bearing fields echoed")
		}
		after, _ := os.ReadFile(filepath.Join(root, ".stack", "config.json"))
		if !bytes.Equal(before, after) {
			t.Fatal("invalid edit changed desired configuration")
		}
	}
}

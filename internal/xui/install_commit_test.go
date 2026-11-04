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

func jsonSubscriptionBody(t *testing.T, c config, plan *provisioningIntent, account provisionedClient) []byte {
	t.Helper()
	var docs []any
	for _, protocol := range []string{"vless", "trojan", "hysteria"} {
		docs = append(docs, map[string]any{"outbounds": []any{expectedSubscriptionOutbounds(c, plan, account)[protocol], map[string]any{"protocol": "freedom", "tag": "direct"}, map[string]any{"protocol": "blackhole", "tag": "block"}}, "routing": json.RawMessage(`{"domainStrategy":"AsIs","rules":[{"type":"field","network":"tcp,udp","outboundTag":"proxy"}]}`)})
	}
	data, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func enableVerificationFixture(t *testing.T, w *installRuntime, dir string) *int {
	t.Helper()
	var plan provisioningIntent
	if ok, err := readJSON(filepath.Join(dir, "provisioning.json"), &plan, true); err != nil || !ok {
		t.Fatal("missing provisioning")
	}
	w.jsonRead = func(ctx context.Context, c config, id string) ([]byte, error) {
		for _, account := range plan.Clients {
			if account.SubID == id {
				return jsonSubscriptionBody(t, c, &plan, account), nil
			}
		}
		return nil, errors.New("unknown subscription")
	}
	count := new(int)
	w.transferProbe = func(ctx context.Context, id, arch string, data []byte) error {
		if !dockerID(id) || arch != "amd64" && arch != "arm64" {
			t.Fatal("invalid probe target")
		}
		var request struct {
			Inbounds []struct {
				Listen string
				Port   int
			}
			Outbounds []struct{ Protocol string }
		}
		if json.Unmarshal(data, &request) != nil || len(request.Inbounds) != 1 || request.Inbounds[0].Listen != "127.0.0.1" || request.Inbounds[0].Port != 39080 || len(request.Outbounds) != 1 {
			t.Fatal("probe escaped its private endpoint")
		}
		*count++
		return ctx.Err()
	}
	return count
}

type installCommitBoundary struct{ *installRuntime }

func (w installCommitBoundary) Steps(ctx context.Context, dir string) ([]deployment.Step, error) {
	steps, err := w.installRuntime.Steps(ctx, dir)
	if err != nil {
		return nil, err
	}
	for i := range steps {
		if steps[i].ID == "commit" {
			run := steps[i].Run
			steps[i].Run = func(ctx context.Context) error {
				if err := run(ctx); err != nil {
					return err
				}
				return errors.New("synthetic loss after applied-state publication")
			}
		}
	}
	return steps, nil
}

func TestInstallationCompletesOnlyAfterVerificationAndReplaysExactCommit(t *testing.T) {
	w, docker, _, dir := promotionWorkflowFixture(t)
	count := enableVerificationFixture(t, w, dir)
	engine := deployment.Engine{Root: w.root}
	if err := engine.Resume(t.Context(), installCommitBoundary{w}); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected interrupted commit checkpoint: %v", err)
	}
	if *count != 6 {
		t.Fatalf("verification and final commit must each check all three transports, got %d", *count)
	}
	path := filepath.Join(w.root, ".stack/applied.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var applied baseline
	if json.Unmarshal(before, &applied) != nil || applied.Version != 1 || len(applied.Hashes) != 3 || checkOutputs(w.root, applied.Hashes) != nil {
		t.Fatal("commit is not compatible with managed-state readers")
	}
	if _, _, err := readDraft(w.root); err == nil {
		t.Fatal("ordinary initialization adopted a completed installation")
	}
	// Simulate a lost engine commit checkpoint. A new runtime opens the frozen
	// operation and admits only its byte-identical baseline during revalidation.
	resumed := *w
	resumed.binding = nil
	if err := engine.Resume(t.Context(), &resumed); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || *count != 9 || docker.publicStarts != 1 || docker.stops != 1 || docker.restarts != 1 {
		t.Fatal("commit replay changed state or repeated lifecycle effects")
	}
	if resumed.commitBaseline != nil {
		t.Fatal("commit permission escaped replay")
	}
}

func TestInstallationNeverCommitsFailedJSONRoutingOrTransfers(t *testing.T) {
	for _, fault := range []string{"json", "routing", "transfer", "commit-transfer", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			w, _, api, dir := promotionWorkflowFixture(t)
			enableVerificationFixture(t, w, dir)
			ctx := t.Context()
			switch fault {
			case "json":
				w.jsonRead = func(context.Context, config, string) ([]byte, error) { return []byte(`[]`), nil }
			case "routing":
				api.routingConfig = map[string]any{"routing": json.RawMessage(strings.Replace(freshRouting, "geoip:private", "geoip:ru", 1)), "outbounds": json.RawMessage(freshOutbounds)}
			case "transfer":
				w.transferProbe = func(context.Context, string, string, []byte) error { return errors.New("unreachable") }
			case "commit-transfer":
				calls := 0
				w.transferProbe = func(context.Context, string, string, []byte) error {
					calls++
					if calls > 3 {
						return errors.New("transfer failed after verify checkpoint")
					}
					return nil
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := (deployment.Engine{Root: w.root}).Resume(ctx, w); err == nil {
				t.Fatal("failed gate completed")
			}
			if _, err := os.Lstat(filepath.Join(w.root, ".stack/applied.json")); !os.IsNotExist(err) {
				t.Fatal("failed gate wrote baseline")
			}
		})
	}
}

func TestCommitReplayRejectsForeignBaselineAndChangedResources(t *testing.T) {
	for _, fault := range []string{"foreign-before", "foreign-after", "target", "resource", "transfer"} {
		t.Run(fault, func(t *testing.T) {
			w, docker, _, dir := promotionWorkflowFixture(t)
			enableVerificationFixture(t, w, dir)
			path := filepath.Join(w.root, ".stack/applied.json")
			if fault != "foreign-before" {
				if err := (deployment.Engine{Root: w.root}).Resume(t.Context(), w); err != nil {
					t.Fatal(err)
				}
			}
			switch fault {
			case "foreign-before", "foreign-after":
				if err := os.WriteFile(path, []byte(`{"version":99}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "target":
				if err := os.WriteFile(filepath.Join(dir, "install-commit.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "resource":
				docker.final.Container.StartedAt = "2026-11-05T00:00:00Z"
				docker.saveFinal()
			case "transfer":
				w.transferProbe = func(context.Context, string, string, []byte) error { return errors.New("down") }
			}
			before, _ := os.ReadFile(path)
			unlock, err := deployment.Lock(w.root)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			snapshot, err := w.open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.commitInstallation(t.Context(), dir, snapshot); err == nil {
				t.Fatal("commit replay accepted drift")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("failed replay rewrote baseline")
			}
		})
	}
}

func TestJSONSubscriptionRejectsConnectionDrift(t *testing.T) {
	w, _, _, dir := promotionWorkflowFixture(t)
	var plan provisioningIntent
	if ok, err := readJSON(filepath.Join(dir, "provisioning.json"), &plan, true); err != nil || !ok {
		t.Fatal(err)
	}
	var c config
	_ = json.Unmarshal(w.binding.Inputs[".stack/config.json"], &c)
	body := jsonSubscriptionBody(t, c, &plan, plan.Clients[0])
	if _, err := checkedJSONOutbounds(body, c, &plan, plan.Clients[0]); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"credential", "endpoint", "insecure", "mux", "routing", "missing"} {
		t.Run(fault, func(t *testing.T) {
			var docs []map[string]any
			_ = json.Unmarshal(body, &docs)
			proxy := docs[0]["outbounds"].([]any)[0].(map[string]any)
			switch fault {
			case "credential":
				proxy["settings"].(map[string]any)["id"] = "wrong"
			case "endpoint":
				proxy["settings"].(map[string]any)["address"] = "other.example"
			case "insecure":
				proxy["streamSettings"].(map[string]any)["tlsSettings"] = map[string]any{"allowInsecure": true}
			case "mux":
				proxy["mux"] = map[string]any{"enabled": true}
			case "routing":
				docs[0]["routing"] = map[string]any{"rules": []any{}}
			case "missing":
				docs = docs[:2]
			}
			changed, _ := json.Marshal(docs)
			if _, err := checkedJSONOutbounds(changed, c, &plan, plan.Clients[0]); err == nil {
				t.Fatal("changed subscription accepted")
			}
		})
	}
}

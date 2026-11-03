package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

func TestPasswordReviewPrivatelyBindsInputsAndRuntimeWithoutEnablingExecution(t *testing.T) {
	root := passwordFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "xui", "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := deployment.WriteJSON(filepath.Join(root, "compatibility.json"), map[string]any{"schema": 1, "panel_image": "ghcr.io/mhsanaei/3x-ui:v3.7.0", "traefik_image": "traefik:v3.7.12"}); err != nil {
		t.Fatal(err)
	}
	started := "2026-01-01T00:00:00Z"
	stack := Stack{docker: func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "exec" {
			if args[1] != strings.Repeat("a", 64) {
				t.Fatal("probe did not use immutable container identity")
			}
			return []byte("v3.7.0"), nil
		}
		image, id := "ghcr.io/mhsanaei/3x-ui:v3.7.0", strings.Repeat("a", 64)
		if args[len(args)-1] == "traefik" {
			image, id = "traefik:v3.7.12", strings.Repeat("b", 64)
		}
		return json.Marshal(map[string]any{
			"id": id, "image_id": "sha256:" + strings.Repeat("c", 64), "image": image, "running": true, "started_at": started,
			"mounts": []any{map[string]any{"Type": "bind", "Source": filepath.Join(root, "xui", "db"), "Destination": "/etc/x-ui", "RW": true}},
			"ports":  map[string]any{"8443/tcp": []any{map[string]string{"HostIp": "127.0.0.1", "HostPort": "2053"}}},
		})
	}}
	if _, enabled := any(stack).(deployment.ExecutionAdapter); enabled {
		t.Fatal("3x-ui writes were enabled before integration validation")
	}
	spec, err := stack.ReviewState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var bound struct {
		Inputs  map[string][]byte  `json:"inputs"`
		Runtime runtimeObservation `json:"runtime"`
	}
	if json.Unmarshal(spec.Binding, &bound) != nil || bound.Runtime.Panel.ID != strings.Repeat("a", 64) || bound.Runtime.Panel.StartedAt != started {
		t.Fatal("review omitted runtime identity")
	}
	var desired config
	if json.Unmarshal(bound.Inputs[".stack/config.json"], &desired) != nil || desired.Env["PANEL_PASS"] != newPassword {
		t.Fatal("review omitted exact private desired credentials")
	}
	e := deployment.Engine{Root: root}
	review, err := e.Review(context.Background(), stack)
	if err != nil || review.CanApply {
		t.Fatal("password review failed or enabled execution")
	}
	public, _ := json.Marshal(review)
	if bytes.Contains(public, []byte(secret)) || bytes.Contains(public, []byte(root)) || bytes.Contains(public, []byte("sha256:")) {
		t.Fatal("private binding crossed public review boundary")
	}
	started = "2026-01-01T00:00:01Z"
	changed, err := stack.ReviewState(context.Background(), root)
	if err != nil || bytes.Equal(changed.Binding, spec.Binding) {
		t.Fatal("container restart did not change review binding")
	}
	if status, err := e.Status(); err != nil || status.Status != "idle" {
		t.Fatal("saving a review registered an execution")
	}
}

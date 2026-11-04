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

func TestRuntimeDiscoveryUsesOwnedPinnedContainersOnly(t *testing.T) {
	for _, scenario := range []string{"compatible", "wrong-image", "wrong-database", "stopped", "wrong-binding", "wrong-version", "inspect-error", "unknown-policy"} {
		t.Run(scenario, func(t *testing.T) {
			root, _, _ := fixture(t)
			if err := os.MkdirAll(filepath.Join(root, "xui", "db"), 0700); err != nil {
				t.Fatal(err)
			}
			policy := map[string]any{"schema": 1, "panel_image": "ghcr.io/mhsanaei/3x-ui:v3.7.0", "traefik_image": "traefik:v3.7.12"}
			if scenario == "unknown-policy" {
				policy["panel_image"] = "ghcr.io/mhsanaei/3x-ui:v99.0.0"
			}
			if err := deployment.WriteJSON(filepath.Join(root, "compatibility.json"), policy); err != nil {
				t.Fatal(err)
			}
			var commands [][]string
			run := func(ctx context.Context, args ...string) ([]byte, error) {
				commands = append(commands, append([]string(nil), args...))
				if args[0] == "exec" {
					if strings.Join(args, " ") != "exec "+strings.Repeat("a", 64)+" /app/x-ui -v" {
						t.Fatal("runtime discovery executed an unreviewed command")
					}
					if scenario == "wrong-image" || scenario == "wrong-database" || scenario == "stopped" {
						t.Fatal("version probe ran before confirming ownership")
					}
					if scenario == "wrong-version" {
						return []byte("private-unexpected-version"), nil
					}
					return []byte("v3.7.0\n"), nil
				}
				if len(args) != 6 || args[0] != "inspect" || args[1] != "--type" || args[2] != "container" || args[3] != "--format" || args[4] != inspectTemplate {
					t.Fatal("unexpected Docker inspection command")
				}
				if strings.Contains(args[4], ".Env") || strings.Contains(args[4], ".Labels") {
					t.Fatal("inspection requested secret-bearing metadata")
				}
				if scenario == "inspect-error" {
					return []byte("private-inspection-output"), errors.New("private-Docker-error")
				}
				name := args[5]
				id := strings.Repeat("a", 64)
				if name == "traefik" {
					id = strings.Repeat("b", 64)
				}
				image := "ghcr.io/mhsanaei/3x-ui:v3.7.0"
				if name == "traefik" {
					image = "traefik:v3.7.12"
				}
				if scenario == "wrong-image" && name == "3x-ui" {
					image = "private-other-image"
				}
				source := filepath.Join(root, "xui", "db")
				if scenario == "wrong-database" {
					source = filepath.Join(root, "private-other-database")
				}
				bind := "127.0.0.1"
				if scenario == "wrong-binding" {
					bind = "0.0.0.0"
				}
				return json.Marshal(map[string]any{
					"id": id, "image_id": "sha256:" + strings.Repeat("c", 64), "image": image, "running": scenario != "stopped", "started_at": "2026-01-01T00:00:00Z",
					"mounts": []any{map[string]any{"Type": "bind", "Source": source, "Destination": "/etc/x-ui/", "RW": true}},
					"ports":  map[string]any{"8443/tcp": []any{map[string]string{"HostIp": bind, "HostPort": "2053"}}},
				})
			}
			before := files(t, root)
			report, err := (Stack{docker: run}).Runtime(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			want := "blocked"
			if scenario == "compatible" {
				want = "compatible"
			}
			if report.Status != want {
				t.Fatalf("runtime status: %s, want %s", report.Status, want)
			}
			out, _ := json.Marshal(report)
			if strings.Contains(string(out), "private-") {
				t.Fatal("raw runtime metadata leaked")
			}
			if scenario == "unknown-policy" && len(commands) != 0 {
				t.Fatal("Docker queried outside supported policy")
			}
			for name, data := range files(t, root) {
				if before[name] != data {
					t.Fatal("runtime discovery changed deployment data")
				}
			}
		})
	}
}

func TestRuntimeDigestMustResolveToPinnedImageID(t *testing.T) {
	policy := "traefik:v3.7.12"
	digest := "traefik@sha256:" + strings.Repeat("a", 64)
	imageID := "sha256:" + strings.Repeat("b", 64)
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) != 5 || strings.Join(args[:4], " ") != "image inspect "+policy+" --format" {
			t.Fatal("unexpected image lookup")
		}
		return json.Marshal(map[string]any{"id": imageID, "digests": []string{digest}})
	}
	for _, scenario := range []struct {
		ref, id string
		want    bool
	}{
		{digest, imageID, true},
		{digest, "sha256:" + strings.Repeat("c", 64), false},
		{"traefik@sha256:" + strings.Repeat("d", 64), imageID, false},
		{"foreign@sha256:" + strings.Repeat("a", 64), imageID, false},
	} {
		if got := matchesPolicyImage(t.Context(), run, container{Image: scenario.ref, ImageID: scenario.id}, policy); got != scenario.want {
			t.Fatal("digest/image identity policy mismatch")
		}
	}
}

func TestRuntimeDiscoveryRejectsMissingManagedStateBeforeDocker(t *testing.T) {
	run := func(context.Context, ...string) ([]byte, error) {
		t.Fatal("Docker queried without managed configuration")
		return nil, nil
	}
	report, err := (Stack{docker: run}).Runtime(context.Background(), t.TempDir())
	if err != nil || report.Status != "blocked" {
		t.Fatal("missing managed state accepted")
	}
}

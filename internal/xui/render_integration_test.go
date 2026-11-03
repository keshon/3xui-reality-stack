//go:build integration

package xui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

// This validates real Compose loading and relative bind resolution without
// creating a network, container, volume or certificate. It shares the explicit
// opt-in used by the existing disposable Linux integration harness.
func TestRenderedComposeConfig(t *testing.T) {
	if os.Getenv("STACK_TEST_CONTAINERS") != "1" {
		t.Skip("set STACK_TEST_CONTAINERS=1 on a disposable Linux Docker test host")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("Compose acceptance test requires Linux")
	}
	for _, mode := range []string{"http", "dns"} {
		t.Run(mode, func(t *testing.T) {
			input := validInitialInput()
			input.ACMEMode = mode
			if mode == "dns" {
				input.DNSToken = `synthetic-${SHOULD_NOT_EXPAND}-token`
			}
			root := filepath.Join(t.TempDir(), "draft")
			if _, err := (Stack{}).Initialize(t.Context(), root, rawInitialInput(t, input)); err != nil {
				t.Fatal(err)
			}
			env := filepath.Join(t.TempDir(), "poison.env")
			if err := os.WriteFile(env, []byte("SHOULD_NOT_EXPAND=unexpected-interpolation\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, project := range []string{"xui", "traefik", "decoy"} {
				args := []string{"--host", "unix:///var/run/docker.sock", "compose", "--env-file", env, "-f", filepath.Join(root, project, "compose.json")}
				if project != "xui" {
					repository := project
					if project == "decoy" {
						repository = "nginx"
					}
					override := filepath.Join(root, ".stack", project+"-test-override.json")
					if err := deployment.WriteJSON(override, webOverride(installRuntimeSnapshot{Root: root, Owner: strings.Repeat("a", 32)}, project, repository+"@sha256:"+strings.Repeat("a", 64))); err != nil {
						t.Fatal(err)
					}
					args = append(args, "-f", override)
				}
				args = append(args, "config", "--format", "json")
				cmd := exec.CommandContext(t.Context(), "docker", args...)
				output, err := cmd.Output()
				if err != nil || !json.Valid(output) {
					t.Fatal("real Compose rejected generated deployment document")
				}
				if bytes.Contains(output, []byte("unexpected-interpolation")) {
					t.Fatal("Compose interpolated a literal private value")
				}
				var resolved struct {
					Services map[string]struct {
						Volumes []struct{ Type, Source, Target string }
					}
				}
				if json.Unmarshal(output, &resolved) != nil {
					t.Fatal("cannot inspect normalized Compose configuration")
				}
				for _, service := range resolved.Services {
					for _, volume := range service.Volumes {
						if volume.Target == "/certs" && volume.Source != filepath.Join(root, "shared/certs") {
							t.Fatal("Compose resolved certificate bind outside this draft")
						}
						if volume.Target == "/etc/x-ui" && volume.Source != filepath.Join(root, "xui/db") {
							t.Fatal("Compose resolved database bind outside this draft")
						}
					}
				}
			}
		})
	}
}

package xui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hashBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func TestInstalledControllerRequiresExactReleaseChecksum(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "duplicate", "mismatch", "dev", "changed-binary"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "stackctl-go")
			data := []byte("synthetic-controller-binary")
			if err := os.WriteFile(binary, data, 0755); err != nil {
				t.Fatal(err)
			}
			line := hashBytes(data) + "  stackctl-go\n"
			manifest := line
			if scenario == "duplicate" {
				manifest += line
			}
			if scenario == "mismatch" {
				manifest = strings.Repeat("a", 64) + "  stackctl-go\n"
			}
			if scenario != "missing" {
				os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(manifest), 0600)
			}
			if scenario == "changed-binary" {
				os.WriteFile(binary, []byte("replacement-private-marker"), 0755)
			}
			version := "v0.1.0-go.1"
			if scenario == "dev" {
				version = "dev"
			}
			observation, err := installedController(t.Context(), binary, version)
			if scenario == "valid" {
				if err != nil || observation.SHA256 != hashBytes(data) || observation.Version != version {
					t.Fatal("valid installed release was rejected")
				}
			} else if err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatal("unverified controller accepted or echoed")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := executableHash(ctx, "unused"); err != context.Canceled {
		t.Fatal("controller hashing ignored cancellation")
	}
}

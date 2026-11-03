package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseManifestMatchesEveryAssetAndNeverOverwrites(t *testing.T) {
	output := filepath.Join(t.TempDir(), "release")
	calls := 0
	run := func(ctx context.Context, target target, version, output string) error {
		calls++
		if version != "v0.1.0-go.1" {
			t.Fatal("release version changed during build")
		}
		return os.WriteFile(output, []byte(target.command+"/"+target.os+"/"+target.arch+"/"+version), 0755)
	}
	if err := build(context.Background(), "v0.1.0-go.1", output, run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "SHA256SUMS"))
	if err != nil || calls != 5 {
		t.Fatal("release did not build all five assets")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatal("manifest does not cover every asset")
	}
	for _, line := range lines {
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || filepath.Base(name) != name {
			t.Fatal("invalid checksum manifest entry")
		}
		asset, err := os.ReadFile(filepath.Join(output, name))
		sum := sha256.Sum256(asset)
		if err != nil || hash != hex.EncodeToString(sum[:]) {
			t.Fatal("manifest does not match asset bytes")
		}
	}
	if err := build(context.Background(), "v0.1.0-go.1", output, run); err == nil || calls != 5 {
		t.Fatal("release builder overwrote an existing output directory")
	}
}

func TestFailedReleaseHasNoCompletionManifest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "release")
	err := build(context.Background(), "v0.1.0-go.1", output, func(context.Context, target, string, string) error {
		return errors.New("private-builder-error")
	})
	if err == nil || strings.Contains(err.Error(), "private-builder-error") {
		t.Fatal("build failure was ignored or raw tool output exposed")
	}
	if _, err := os.Stat(filepath.Join(output, "SHA256SUMS")); !os.IsNotExist(err) {
		t.Fatal("failed release received a completion manifest")
	}
}

func TestReleaseVersionsCannotInjectFlagsOrPaths(t *testing.T) {
	for _, version := range []string{"", "latest", "v01.0.0", "v1.0.0/other", "v1.0.0;private", "v1.0.0 -X private", "v1.0.0\nprivate", strings.Repeat("v", 81)} {
		if ValidVersion(version) {
			t.Fatal("unsafe release version accepted")
		}
	}
	for _, version := range []string{"v0.0.0", "v1.2.3", "v0.1.0-go.1", "v1.2.3-rc-next.2"} {
		if !ValidVersion(version) {
			t.Fatal("supported version rejected")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(t.TempDir(), "release")
	if err := Build(ctx, "v0.1.0-go.1", output); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled build started")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("cancelled build created output")
	}
}

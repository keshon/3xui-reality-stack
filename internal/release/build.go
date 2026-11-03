// Package release builds controller/manager assets without application state.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)

func ValidVersion(version string) bool {
	return len(version) <= 80 && versionPattern.MatchString(version)
}

type target struct{ command, os, arch, suffix string }

var targets = []target{
	{"stack-manager", "linux", "amd64", ""},
	{"stack-manager", "linux", "arm64", ""},
	{"stack-manager", "windows", "amd64", ".exe"},
	{"stackctl-go", "linux", "amd64", ""},
	{"stackctl-go", "linux", "arm64", ""},
}

type builder func(context.Context, target, string, string) error

// Build creates a new output directory. SHA256SUMS is the completion marker;
// existing directories are never overwritten, even after an interrupted build.
func Build(ctx context.Context, version, output string) error {
	return build(ctx, version, output, compile)
}

func build(ctx context.Context, version, output string, run builder) error {
	if !ValidVersion(version) || output == "" {
		return errors.New("use a version such as v0.1.0-go.1 and a new output directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(output, 0755); err != nil {
		return errors.New("release output must be a new directory with an existing parent")
	}
	var manifest strings.Builder
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := fmt.Sprintf("%s_%s_%s%s", t.command, t.os, t.arch, t.suffix)
		path := filepath.Join(output, name)
		if err := run(ctx, t, version, path); err != nil {
			return fmt.Errorf("release build failed for %s; no completion manifest was written", name)
		}
		f, err := os.Open(path)
		if err != nil {
			return errors.New("release asset is missing")
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("cannot hash release asset")
		}
		fmt.Fprintf(&manifest, "%s  %s\n", hex.EncodeToString(hash.Sum(nil)), name)
	}
	return os.WriteFile(filepath.Join(output, "SHA256SUMS"), []byte(manifest.String()), 0644)
}

func compile(ctx context.Context, t target, version, output string) error {
	flags := "-s -w -X github.com/keshon/3xui-reality-stack/internal/buildinfo.Version=" + version
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", output, "./cmd/"+t.command)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOOS") || strings.EqualFold(key, "GOARCH") || strings.EqualFold(key, "CGO_ENABLED") || strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOOS="+t.os, "GOARCH="+t.arch, "CGO_ENABLED=0", "GOFLAGS=")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

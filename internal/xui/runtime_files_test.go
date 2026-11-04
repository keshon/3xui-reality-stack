package xui

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestInitializedRuntimeLayoutKeepsSecretsPrivateAndSiteReadable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	raw := rawInitialInput(t, validInitialInput())
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"xui/db", "traefik/data/logs", "shared/certs/certs", "shared/certs/private"} {
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil || len(entries) != 0 {
			t.Fatal("runtime bind directory missing or unexpectedly populated")
		}
	}
	data, err := os.ReadFile(filepath.Join(root, acmeStateFile))
	if err != nil || len(data) != 0 {
		t.Fatal("fresh ACME state was not initialized empty")
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{
			".": 0700, ".env": 0600, ".stack/config.json": 0600,
			"decoy/site": 0755, "decoy/site/index.html": 0644, "decoy/nginx.conf": 0644,
			"traefik/dynamic": 0755, "traefik/dynamic/tls.yml": 0644,
			"xui/db": 0700, "traefik/data/acme": 0700, acmeStateFile: 0600,
			"shared/certs/certs": 0755, "shared/certs/private": 0700,
		} {
			info, err := os.Stat(filepath.Join(root, name))
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("wrong runtime permissions on %s", name)
			}
		}
	}
	before := files(t, root)
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, files(t, root)) {
		t.Fatal("rerun changed draft runtime files")
	}
}

func TestInitializationNeverResetsACMEState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	raw := rawInitialInput(t, validInitialInput())
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, acmeStateFile), []byte("private-live-acme-state"), 0600); err != nil {
		t.Fatal(err)
	}
	before := files(t, root)
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err == nil || strings.Contains(err.Error(), "private-live-acme-state") {
		t.Fatal("initializer accepted or echoed populated ACME state")
	}
	if !reflect.DeepEqual(before, files(t, root)) {
		t.Fatal("initializer reset active ACME state")
	}
}

func TestInitializationRejectsRedirectedEmptyBindDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "draft")
	raw := rawInitialInput(t, validInitialInput())
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err != nil {
		t.Fatal(err)
	}
	bind := filepath.Join(root, "xui/db")
	if err := os.Remove(bind); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, bind); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := (Stack{}).Initialize(t.Context(), root, raw); err == nil {
		t.Fatal("initializer accepted redirected database bind")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("initializer touched redirected bind directory")
	}
}

func TestInitializationRefusesPermissionDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions required")
	}
	for _, target := range []string{".env", "shared/certs/private"} {
		t.Run(target, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "draft")
			raw := rawInitialInput(t, validInitialInput())
			if _, err := (Stack{}).Initialize(t.Context(), root, raw); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, target)
			if err := os.Chmod(path, 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := (Stack{}).Initialize(t.Context(), root, raw); err == nil {
				t.Fatal("initializer accepted changed private permissions")
			}
		})
	}
}

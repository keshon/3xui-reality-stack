package xui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Runtime paths are prepared only within a new unpublished initialization
// stage. Active installations must never use this to seed/reset ACME state.
var runtimeDirectories = []string{
	"xui/db", "traefik/data", "traefik/data/logs", "traefik/data/acme",
	"shared", "shared/certs", "shared/certs/certs", "shared/certs/private",
}

const acmeStateFile = "traefik/data/acme/acme.json"

func directoryMode(name string) os.FileMode {
	switch name {
	case "decoy/site", "traefik/dynamic", "shared/certs/certs":
		return 0755
	default:
		return 0700
	}
}

func artifactMode(name string) os.FileMode {
	if name == "decoy/nginx.conf" || name == "traefik/dynamic/tls.yml" || strings.HasPrefix(name, "decoy/site/") {
		return 0644
	}
	return 0600
}

func validateDraftDirectories(root string) error {
	for _, name := range append(append([]string(nil), renderedDirectories...), runtimeDirectories...) {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != directoryMode(name) {
			return errors.New("initialization draft directory layout or permissions changed")
		}
	}
	return nil
}

func prepareDraftDirectories(stage string) error {
	for _, name := range append(append([]string(nil), renderedDirectories...), runtimeDirectories...) {
		mode := directoryMode(name)
		if err := os.Mkdir(filepath.Join(stage, name), mode); err != nil {
			return errors.New("cannot prepare private deployment directories")
		}
		// Fix public asset readability even with the installer's private umask.
		if err := os.Chmod(filepath.Join(stage, name), mode); err != nil {
			return errors.New("cannot set deployment directory permissions")
		}
	}
	return nil
}

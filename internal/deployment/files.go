package deployment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// PublishDirectory publishes a complete private staging tree and syncs its
// parent. Existing contents are never replaced; a complete draft on disk
// permits retry after an ambiguous publication outcome.
func PublishDirectory(stage, target string) error {
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("initialization target already exists or cannot be inspected")
	}
	if os.Rename(stage, target) != nil {
		return errors.New("cannot publish initialization draft; existing targets are not overwritten")
	}
	return syncDirectory(filepath.Dir(target))
}

// SyncDirectory persists directory entries on Linux. Windows follows the same
// platform policy as atomic state writes, where directory sync is unavailable.
func SyncDirectory(path string) error { return syncDirectory(path) }

// WriteFile commits a private file by rename and syncs the containing directory.
// Callers own the parent directory and must hold the deployment lock.
func WriteFile(path string, data []byte) error {
	return WriteFileMode(path, data, 0600)
}

// WriteFileMode also supports public static assets. Permissions are applied
// before syncing and renaming, so publication never exposes a partial file.
func WriteFileMode(path string, data []byte, mode os.FileMode) error {
	if mode != 0600 && mode != 0644 {
		return errors.New("state files must use private or public-read permissions")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("refusing to replace a non-regular state file")
	} else if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot inspect state file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".stack-write-")
	if err != nil {
		return errors.New("cannot create private state file")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if f.Chmod(mode) != nil {
		return errors.New("cannot protect state file")
	}
	if _, err := f.Write(data); err != nil {
		return errors.New("cannot write state file")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("cannot sync state file")
	}
	if os.Rename(f.Name(), path) != nil {
		return errors.New("cannot commit state file")
	}
	return syncDirectory(filepath.Dir(path))
}

func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("cannot encode state")
	}
	return WriteFile(path, append(data, '\n'))
}

// privateDirectory rejects symlinks. Its parent must already be owned by us.
func privateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return errors.New("cannot create state directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid state directory")
	}
	if err := os.Chmod(path, 0700); err != nil {
		return errors.New("cannot protect state directory")
	}
	return syncDirectory(filepath.Dir(path))
}

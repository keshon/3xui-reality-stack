package deployment

import (
	"errors"
	"os"
	"path/filepath"
)

// Lock uses the same file and OS locking primitive as stackctl.config.Store.
// Keep it held across preflight, remote actions, verification and local commit.
func Lock(root string) (func(), error) {
	dir := filepath.Join(root, ".stack")
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("invalid deployment lock")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open deployment lock")
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, errors.New("another controller holds the deployment lock")
	}
	return func() { unlockFile(f); f.Close() }, nil
}

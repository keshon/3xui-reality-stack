package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// ServiceInstaller installs only operation-marked helper units. Production
// command wiring deliberately does not expose it until install validation ends.
type ServiceInstaller struct {
	directory string
	observe   func(context.Context, string) (ServiceObservation, error)
	run       func(context.Context, ...string) error
}

func NewServiceInstaller() *ServiceInstaller {
	return &ServiceInstaller{directory: "/etc/systemd/system", observe: ObserveService, run: func(ctx context.Context, args ...string) error {
		_, err := systemctlLocal(ctx, false, append([]string{"--system", "--no-pager"}, args...)...)
		return err
	}}
}

func (s *ServiceInstaller) Ensure(ctx context.Context, name string, data []byte, owner string) error {
	id, err := hex.DecodeString(owner)
	if !serviceName.MatchString(name) || err != nil || len(id) != 16 || len(data) == 0 || len(data) > 64<<10 {
		return errors.New("invalid owned helper service")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(s.directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return errors.New("helper unit directory is unavailable or symlinked")
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return errors.New("cannot open helper unit directory")
	}
	defer root.Close()
	expected := append([]byte("# stackctl-operation "+owner+"\n"), data...)
	check := func() (bool, error) {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > (64<<10)+100 || runtime.GOOS != "windows" && info.Mode().Perm() != 0644 {
			return false, errors.New("helper unit is not an owned regular file")
		}
		current, err := root.ReadFile(name)
		if err != nil || !bytes.Equal(current, expected) {
			return false, errors.New("existing helper unit belongs to another operation or changed")
		}
		return true, nil
	}
	exists, err := check()
	if err != nil {
		return err
	}
	state, err := s.observe(ctx, name)
	if err != nil {
		return errors.New("cannot establish helper service ownership")
	}
	target := filepath.Join(s.directory, name)
	ownedLoaded := func(state ServiceObservation) bool {
		return state.LoadState == "loaded" && state.Fragment == target && state.DropIns == "" && (state.FileState == "enabled" || state.FileState == "disabled")
	}
	if !exists {
		if !state.Absent() {
			return errors.New("existing system service cannot be adopted")
		}
		tmp := ".stackctl-service-" + rand.Text()
		f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("cannot stage helper unit")
		}
		defer root.Remove(tmp)
		_, writeErr := f.Write(expected)
		modeErr := f.Chmod(0644)
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil {
			return errors.New("cannot synchronize helper unit")
		}
		// A hard link publishes the completed file without replacing a competing
		// file or symlink. Both paths are on the same filesystem.
		if err := root.Link(tmp, name); err != nil {
			return errors.New("helper unit appeared during installation; inspect before retrying")
		}
		if runtime.GOOS != "windows" {
			dir, err := root.Open(".")
			if err != nil {
				return errors.New("cannot sync helper unit directory")
			}
			err = dir.Sync()
			dir.Close()
			if err != nil {
				return errors.New("cannot sync helper unit directory")
			}
		}
	} else if !state.Absent() && !ownedLoaded(state) {
		return errors.New("loaded helper service or drop-ins do not match the owned unit")
	}
	state, err = s.observe(ctx, name)
	if err != nil || !state.Absent() && !ownedLoaded(state) {
		return errors.New("helper service ownership changed before activation")
	}
	if exists && ownedLoaded(state) && state.ActiveState == "active" && state.FileState == "enabled" {
		present, err := check()
		if err != nil || !present {
			return errors.New("owned helper unit disappeared or changed")
		}
		return nil
	}
	if err := s.run(ctx, "daemon-reload"); err != nil {
		return errors.New("helper daemon reload failed; retry the owned installation")
	}
	state, err = s.observe(ctx, name)
	if err != nil || !ownedLoaded(state) {
		return errors.New("loaded helper unit differs from the owned installation")
	}
	if present, err := check(); err != nil || !present {
		return errors.New("owned helper unit disappeared or changed")
	}
	// An interrupted reply can still have enabled and started the exact unit.
	_ = s.run(ctx, "enable", "--now", "--", name)
	state, err = s.observe(ctx, name)
	if err != nil || !ownedLoaded(state) || state.ActiveState != "active" || state.FileState != "enabled" {
		return errors.New("owned helper service is not enabled and active; retry after inspection")
	}
	present, err := check()
	if err != nil || !present {
		return errors.New("owned helper unit disappeared or changed")
	}
	return nil
}

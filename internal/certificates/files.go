package certificates

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func syncDirectory(root *os.Root, name string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return errors.New("cannot open certificate directory for synchronization")
	}
	defer f.Close()
	if f.Sync() != nil {
		return errors.New("cannot synchronize certificate directory")
	}
	return nil
}

func prepareDirectory(root *os.Root, name string, mode os.FileMode) error {
	if err := root.Mkdir(name, mode); err != nil && !os.IsExist(err) {
		return errors.New("cannot create certificate directory")
	}
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("certificate directory must not be a symlink or non-directory")
	}
	f, err := root.Open(name)
	if err != nil {
		return errors.New("cannot open certificate directory")
	}
	defer f.Close()
	if f.Chmod(mode) != nil {
		return errors.New("cannot protect certificate directory")
	}
	return syncDirectory(root, ".")
}

type stagedFile struct {
	root               *os.Root
	target, temporary  string
	permissionsChanged bool
}

func stageFile(root *os.Root, target string, data []byte, mode os.FileMode) (*stagedFile, error) {
	staged := &stagedFile{root: root, target: target}
	info, err := root.Lstat(target)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > maxACMEBytes {
			return nil, errors.New("certificate output is not a regular bounded file")
		}
		current, err := root.ReadFile(target)
		if err != nil {
			return nil, errors.New("cannot inspect existing certificate output")
		}
		if bytes.Equal(current, data) {
			f, err := root.OpenFile(target, os.O_RDWR, 0)
			if err != nil {
				return nil, errors.New("cannot open existing certificate output")
			}
			defer f.Close()
			if f.Chmod(mode) != nil || f.Sync() != nil {
				return nil, errors.New("cannot protect existing certificate output")
			}
			staged.permissionsChanged = runtime.GOOS != "windows" && info.Mode().Perm() != mode
			return staged, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("cannot inspect certificate output")
	}
	tmp := filepath.Join(filepath.Dir(target), ".certificate-"+rand.Text())
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("cannot stage certificate output")
	}
	staged.temporary = tmp
	success := false
	defer func() {
		f.Close()
		if !success {
			staged.cleanup()
		}
	}()
	if _, err := f.Write(data); err != nil {
		return nil, errors.New("cannot write staged certificate output")
	}
	if f.Chmod(mode) != nil || f.Sync() != nil || f.Close() != nil {
		return nil, errors.New("cannot synchronize staged certificate output")
	}
	success = true
	return staged, nil
}

func (f *stagedFile) cleanup() {
	if f.temporary != "" {
		_ = f.root.Remove(f.temporary)
	}
}

func (f *stagedFile) commit() (bool, error) {
	if f.temporary == "" {
		return f.permissionsChanged, nil
	}
	if err := f.root.Rename(f.temporary, f.target); err != nil {
		return false, errors.New("cannot publish certificate output; retry export to recover")
	}
	f.temporary = ""
	if err := syncDirectory(f.root, filepath.Dir(f.target)); err != nil {
		return false, err
	}
	return true, nil
}

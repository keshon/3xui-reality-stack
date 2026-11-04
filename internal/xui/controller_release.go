package xui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/keshon/3xui-reality-stack/internal/buildinfo"
)

func executableHash(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(name)
	if err != nil {
		return "", errors.New("cannot read controller executable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return "", errors.New("controller executable is not a bounded regular file")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (128<<20)+1))
	if err != nil || n > 128<<20 {
		return "", errors.New("cannot hash bounded controller executable")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func installedController(ctx context.Context, executable, version string) (controllerObservation, error) {
	if version == "" || version == "dev" || !filepath.IsAbs(executable) {
		return controllerObservation{}, errors.New("fresh-install review requires an installed release controller")
	}
	physical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return controllerObservation{}, errors.New("cannot resolve installed controller")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && (info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0) {
		return controllerObservation{}, errors.New("installed controller is not a protected executable")
	}
	digest, err := executableHash(ctx, physical)
	if err != nil {
		return controllerObservation{}, err
	}
	manifest := filepath.Join(filepath.Dir(physical), "SHA256SUMS")
	info, err = os.Lstat(manifest)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return controllerObservation{}, errors.New("installed controller checksum manifest unavailable")
	}
	f, err := os.Open(manifest)
	if err != nil {
		return controllerObservation{}, errors.New("cannot open installed controller checksum manifest")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return controllerObservation{}, errors.New("cannot read bounded controller checksum manifest")
	}
	matched := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == filepath.Base(physical) {
			if len(fields) != 2 || fields[0] != digest {
				return controllerObservation{}, errors.New("installed controller checksum does not match its manifest")
			}
			matched++
		}
	}
	if matched != 1 {
		return controllerObservation{}, errors.New("installed controller checksum entry missing or ambiguous")
	}
	return controllerObservation{Path: physical, SHA256: digest, Version: version}, nil
}

func currentController(ctx context.Context) (controllerObservation, error) {
	executable, err := os.Executable()
	if err != nil {
		return controllerObservation{}, errors.New("running controller path unavailable")
	}
	controller, err := installedController(ctx, executable, buildinfo.Version)
	if err != nil {
		return controllerObservation{}, err
	}
	if runtime.GOOS != "linux" {
		return controllerObservation{}, errors.New("install review requires a Linux controller")
	}
	running, err := executableHash(ctx, "/proc/self/exe")
	if err != nil || running != controller.SHA256 {
		return controllerObservation{}, errors.New("installed controller differs from the running executable")
	}
	return controller, nil
}

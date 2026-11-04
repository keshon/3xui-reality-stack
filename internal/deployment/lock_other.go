//go:build !linux && !windows

package deployment

import (
	"errors"
	"os"
)

func lockFile(*os.File) error    { return errors.New("unsupported controller platform") }
func unlockFile(*os.File)        {}
func syncDirectory(string) error { return errors.New("unsupported controller platform") }

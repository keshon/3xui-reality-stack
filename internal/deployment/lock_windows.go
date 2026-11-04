package deployment

import (
	"os"
	"syscall"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")
var lockFileEx = kernel.NewProc("LockFileEx")
var unlockFileEx = kernel.NewProc("UnlockFileEx")

// msvcrt.locking locks the first byte. LockFileEx overlaps that same byte.
func lockFile(f *os.File) error {
	o := syscall.Overlapped{}
	ok, _, err := lockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&o)))
	if ok == 0 {
		return err
	}
	return nil
}
func unlockFile(f *os.File) {
	o := syscall.Overlapped{}
	_, _, _ = unlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&o)))
}

// Directory fsync is unavailable through the standard Windows file API.
// Windows supports local development; production writes target Linux.
func syncDirectory(string) error { return nil }

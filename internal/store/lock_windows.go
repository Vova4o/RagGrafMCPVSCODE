//go:build windows

package store

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx       = kernel32.NewProc("LockFileEx")
	unlockFileEx     = kernel32.NewProc("UnlockFileEx")
	lockViolationErr = syscall.Errno(33)
)

func tryFileLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileEx.Call(
		file.Fd(),
		lockFileExclusiveLock|lockFileFailImmediately,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result != 0 {
		return true, nil
	}
	if callErr == lockViolationErr {
		return false, nil
	}
	return false, callErr
}

func unlockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := unlockFileEx.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}

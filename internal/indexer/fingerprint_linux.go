//go:build linux

package indexer

import (
	"io/fs"
	"syscall"
)

// fingerprintCtimeNanos returns the inode change time in nanoseconds, or 0
// when the underlying stat structure is unavailable.
func fingerprintCtimeNanos(info fs.FileInfo) int64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return stat.Ctim.Sec*1e9 + stat.Ctim.Nsec
}

//go:build !darwin && !linux

package indexer

import "io/fs"

// fingerprintCtimeNanos returns 0 on platforms without a portable way to read
// the inode change time (for example Windows, which has no ctime concept).
func fingerprintCtimeNanos(fs.FileInfo) int64 {
	return 0
}

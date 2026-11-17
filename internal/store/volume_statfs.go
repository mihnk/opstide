//go:build linux || darwin

package store

import (
	"path/filepath"
	"syscall"
)

// volume reports the size of the filesystem the database is on and the space left on it for OpsTide (blocks
// available to a non-root user); ok is false when it cannot be read.
func volume(path string) (size, free int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return 0, 0, false
	}
	// Bsize is int64 on Linux and uint32 on macOS, and never negative.
	bs := uint64(st.Bsize)
	return int64(st.Blocks * bs), int64(st.Bavail * bs), true
}

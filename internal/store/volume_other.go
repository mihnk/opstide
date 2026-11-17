//go:build !linux && !darwin

package store

// volume is not read on this platform: the volume metrics are left out.
func volume(string) (size, free int64, ok bool) { return 0, 0, false }

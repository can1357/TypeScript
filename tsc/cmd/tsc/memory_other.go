//go:build !linux

package main

// cgroupMemoryLimit reports no limit: cgroups only exist on Linux.
func cgroupMemoryLimit() (limit uint64, known bool) {
	return 0, true
}

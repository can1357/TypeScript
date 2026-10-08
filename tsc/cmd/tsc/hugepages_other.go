//go:build !linux

package main

// reserveHugePageHeap only applies to Linux transparent huge pages.
func reserveHugePageHeap(memory uint64) {}

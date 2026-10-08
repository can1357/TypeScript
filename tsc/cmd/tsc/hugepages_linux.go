package main

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	hugePageBytes = 2 << 20
	// Smaller reservations do not pay for the forced collection that releases them.
	minHugePageReservation = 64 << 20
	// The runtime's metadata for the reservation (about 0.1% of its size) stays allocated, also for
	// small compilations; 8GB covers the heaps of the largest projects measured (VS Code: ~7GB).
	maxHugePageReservation = 8 << 30
)

// reserveHugePageHeap prepares the address space the Go heap grows into next so that the kernel
// backs it with 2MB transparent huge pages.
//
// In the "madvise" THP mode (the default on many distributions), the kernel uses huge pages
// only where a process asked for them with MADV_HUGEPAGE, and the Go runtime never asks for
// its heap. A compilation fills gigabytes of fresh heap; with 4KB pages that costs hundreds of
// thousands of page faults plus frequent TLB misses in every checker.
//
// The runtime keeps freed heap address space mapped and hands it out again, lowest address
// first. So allocating one large object, advising its pages and freeing it leaves an advised
// range that later allocations fill. The object's pages are never touched, so the reservation
// costs no physical memory; debug.FreeOSMemory returns them to the runtime's released state so
// that they do not count against the memory limit. The object comes from strings.Builder.Grow,
// which allocates without zeroing: make zeroes a new object whenever the runtime reuses freed
// pages for it, and would then touch every page of the reservation.
//
// The reservation is at most three quarters of the available memory, the heap's size limit until
// its first collection (configureBatchGC), and at most the memory currently free in blocks of 2MB
// or more, so that huge page faults do not wait for the kernel to compact memory. The runtime's
// metadata for the range does take memory (about 0.1% of its size), so it is also at most an
// explicit GOMEMLIMIT and maxHugePageReservation. It is skipped where mapping it could fail (strict
// overcommit accounting, an address space or data size limit, a 32-bit address space) and under
// GODEBUG=disablethp=1, which asks for the heap to stay off huge pages.
func reserveHugePageHeap(memory uint64) {
	size := hugePageReservation(memory)
	if size < minHugePageReservation {
		return
	}
	var reserve strings.Builder
	reserve.Grow(int(size))
	// One written byte exposes the buffer's address.
	reserve.WriteByte(0)
	_ = unix.Madvise(unsafe.Slice(unsafe.StringData(reserve.String()), size), unix.MADV_HUGEPAGE)
	reserve.Reset()
	debug.FreeOSMemory()
}

func hugePageReservation(memory uint64) uint64 {
	if strconv.IntSize < 64 || thpDisabledByGODEBUG() || !thpMode("/sys/kernel/mm/transparent_hugepage/enabled", "madvise") {
		return 0
	}
	// Kernels with 16KB or 64KB base pages have larger huge pages (32MB, 512MB), for which the
	// free-memory bound below does not hold.
	if size, err := os.ReadFile("/sys/kernel/mm/transparent_hugepage/hpage_pmd_size"); err != nil || strings.TrimSpace(string(size)) != strconv.Itoa(hugePageBytes) {
		return 0
	}
	if overcommit, err := os.ReadFile("/proc/sys/vm/overcommit_memory"); err != nil || strings.TrimSpace(string(overcommit)) == "2" {
		return 0
	}
	for _, resource := range []int{unix.RLIMIT_AS, unix.RLIMIT_DATA} {
		var limit unix.Rlimit
		if unix.Getrlimit(resource, &limit) != nil || limit.Cur != unix.RLIM_INFINITY {
			return 0
		}
	}
	free := freeHugePageBytes()
	// SetMemoryLimit with a negative value only reports the limit; without one it is MaxInt64.
	limit := uint64(debug.SetMemoryLimit(-1))
	return min(memory/4*3, free, limit, maxHugePageReservation) &^ (hugePageBytes - 1)
}

// thpDisabledByGODEBUG reports whether GODEBUG=disablethp=1 asks the runtime to keep the heap off
// huge pages (its last disablethp setting wins).
func thpDisabledByGODEBUG() bool {
	disabled := false
	for setting := range strings.SplitSeq(os.Getenv("GODEBUG"), ",") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(setting), "disablethp="); ok {
			disabled = value == "1"
		}
	}
	return disabled
}

// thpMode reports whether the THP setting file at path selects mode ("[mode]").
func thpMode(path string, mode string) bool {
	text, err := os.ReadFile(path)
	return err == nil && bytes.Contains(text, []byte("["+mode+"]"))
}

// freeHugePageBytes sums the free memory in blocks of 2MB or more over all zones of /proc/buddyinfo,
// whose lines read "Node 0, zone Normal <free blocks of order 0> <order 1> ...".
func freeHugePageBytes() uint64 {
	file, err := os.Open("/proc/buddyinfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	pageBytes := uint64(os.Getpagesize())
	var free uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[2] != "zone" {
			continue
		}
		for order, field := range fields[4:] {
			blockBytes := pageBytes << order
			if blockBytes < hugePageBytes {
				continue
			}
			count, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0
			}
			free = min(free+count*blockBytes, math.MaxUint64/2)
		}
	}
	return free
}

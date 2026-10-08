package core

import (
	"os"
	"unsafe"
)

var osPageSize = uintptr(os.Getpagesize())

// prefault writes a zero byte to every memory page spanned by the freshly allocated slice s (its
// full capacity), so that the operating system maps each page writable on its first touch.
//
// Fresh heap memory is not zeroed by the Go runtime because the operating system supplies it
// zeroed. On Linux, the first read of such a page maps the shared zero page; the first write
// after that must replace a present mapping with a copy-on-write fault, which flushes the TLB
// of every CPU running the process. Arena-allocated values are almost always read first (nil
// checks, lazily initialized fields), so without this every fresh page costs two faults and a
// cross-CPU TLB shootdown that interrupts all concurrently running checkers.
//
// Only call prefault on memory nobody else references yet: the zero bytes it writes are no-ops
// solely because the memory is known to be zero.
func prefault[T any](s []T) {
	var zero T
	size := uintptr(cap(s)) * unsafe.Sizeof(zero)
	if size == 0 {
		return
	}
	base := unsafe.Pointer(unsafe.SliceData(s[:cap(s)]))
	*(*byte)(base) = 0
	// Offsets of the following page boundaries within the allocation.
	for offset := osPageSize - uintptr(base)&(osPageSize-1); offset < size; offset += osPageSize {
		*(*byte)(unsafe.Add(base, offset)) = 0
	}
}

// newPrefaulted allocates a zeroed value of type T whose memory pages are prefaulted; see prefault.
func newPrefaulted[T any]() *T {
	p := new(T)
	prefault(unsafe.Slice(p, 1))
	return p
}

package main

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"

	osmemory "github.com/mackerelio/go-osstat/memory"
)

// batchSys is the CLI system; it opts batch compilations into process-wide runtime tuning.
type batchSys struct{ *osSys }

func (s *batchSys) BeforeBatchCompilation() func() {
	var total uint64
	if memory, err := osmemory.Get(); err == nil {
		total = memory.Total
	}
	// Physical memory overstates what a container may use. Without a known limit, the tuning
	// could use more memory than the container has, so the runtime keeps its defaults.
	limit, known := cgroupMemoryLimit()
	if !known {
		return func() {}
	}
	total = minLimit(total, limit)
	return configureBatchGC(total).restore
}

// minLimit returns the smaller of two memory limits, where 0 means none.
func minLimit(a, b uint64) uint64 {
	if a == 0 || b != 0 && b < a {
		return b
	}
	return a
}

// batchGCPercent is the GOGC value of batch compilations. Batch compiles retain most of their
// heap until exit, so GOGC=300 trades a small increase in peak RSS for far fewer collections.
const batchGCPercent = 300

// batchGC paces the collector for one batch compilation: GOGC=300 under a soft memory limit.
//
// The soft limit makes the collector work harder before the machine runs short (it is not an
// RSS cap). It is half of physical memory, but after each collection at least the heap goal
// of the runtime's default GOGC=100, twice the live heap (batchMemoryLimit): a fixed limit
// close above the live heap would make the collector run back to back, far more often than by
// default. So the limit only ever lowers GOGC=300's goal toward the default one.
//
// GOGC is only raised under a memory limit. Explicit GOGC/GOMEMLIMIT environment settings
// always take precedence: with GOGC set, the runtime keeps its settings; with GOMEMLIMIT alone,
// GOGC=300 applies under that limit.
type batchGC struct {
	mu         sync.Mutex
	softLimit  int64
	done       bool // restore ran; collections no longer adjust the settings.
	restoreGC  func()
	restoreMem func()
}

// gcSentinel is a heap object whose cleanup reports a collection. It holds a pointer so that it is
// never allocated by the tiny allocator, whose objects may never be reported.
type gcSentinel struct{ _ *byte }

// batchMemoryLimit is the memory limit after a collection that left live bytes of heap: the soft
// limit, or the default GOGC=100 heap goal if that is higher.
func batchMemoryLimit(softLimit int64, live uint64) int64 {
	return max(softLimit, int64(min(live, math.MaxInt64/2))*2)
}

func configureBatchGC(memory uint64) *batchGC {
	g := &batchGC{restoreGC: func() {}, restoreMem: func() {}}
	_, explicitGC := os.LookupEnv("GOGC")
	_, explicitLimit := os.LookupEnv("GOMEMLIMIT")
	// An explicit GOGC opts out entirely: a fixed memory limit near a large live heap would make the
	// collector run back to back, and only collections under this policy raise the limit.
	changeLimit := !explicitLimit && !explicitGC && memory > 0
	if changeLimit {
		g.softLimit = int64(min(memory/2, uint64(math.MaxInt64)))
		old := debug.SetMemoryLimit(g.softLimit)
		g.restoreMem = func() { debug.SetMemoryLimit(old) }
		runtime.AddCleanup(new(gcSentinel), (*batchGC).collected, g)
	}
	if !explicitGC && (changeLimit || explicitLimit) {
		old := debug.SetGCPercent(batchGCPercent)
		g.restoreGC = func() { debug.SetGCPercent(old) }
	}
	return g
}

// collected runs after each collection: it sets the memory limit for the new live heap and waits
// for the next collection.
func (g *batchGC) collected() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done {
		return
	}
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	debug.SetMemoryLimit(batchMemoryLimit(g.softLimit, sample[0].Value.Uint64()))
	runtime.AddCleanup(new(gcSentinel), (*batchGC).collected, g)
}

// restore puts back the runtime settings from before configureBatchGC.
func (g *batchGC) restore() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done = true
	g.restoreGC()
	g.restoreMem()
}

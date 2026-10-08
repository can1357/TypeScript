package main

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// gcSettings reads the current GOGC percentage and memory limit without changing them.
func gcSettings() (int, int64) {
	gc := debug.SetGCPercent(-1)
	debug.SetGCPercent(gc)
	return gc, debug.SetMemoryLimit(-1)
}

// setGCEnv makes GOGC and GOMEMLIMIT unset (nil) or set to the given values for the test, and
// gives the runtime known settings that the test restores.
func setGCEnv(t *testing.T, gcEnv, limitEnv *string) {
	t.Helper()
	for name, value := range map[string]*string{"GOGC": gcEnv, "GOMEMLIMIT": limitEnv} {
		t.Setenv(name, "")
		if value == nil {
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Setenv(name, *value)
		}
	}
	oldGC := debug.SetGCPercent(100)
	oldLimit := debug.SetMemoryLimit(16 << 30)
	t.Cleanup(func() {
		debug.SetGCPercent(oldGC)
		debug.SetMemoryLimit(oldLimit)
	})
}

func TestConfigureBatchGCPrecedence(t *testing.T) {
	for _, test := range []struct {
		name            string
		gcEnv, limitEnv *string
		memory          uint64
		wantGC          int
		wantLimit       int64
	}{
		{name: "unknown memory", wantGC: 100, wantLimit: 16 << 30},
		{name: "explicit GC", gcEnv: new("50"), memory: 8 << 30, wantGC: 100, wantLimit: 16 << 30},
		{name: "explicit limit", limitEnv: new("1GiB"), memory: 8 << 30, wantGC: 300, wantLimit: 16 << 30},
		{name: "both explicit", gcEnv: new("off"), limitEnv: new("off"), memory: 8 << 30, wantGC: 100, wantLimit: 16 << 30},
		{name: "empty is explicit", gcEnv: new(""), limitEnv: new(""), memory: 8 << 30, wantGC: 100, wantLimit: 16 << 30},
	} {
		t.Run(test.name, func(t *testing.T) {
			setGCEnv(t, test.gcEnv, test.limitEnv)
			g := configureBatchGC(test.memory)
			// Only a deferred collector reacts to the program size.
			g.programBound()
			if gc, limit := gcSettings(); gc != test.wantGC || limit != test.wantLimit {
				t.Fatalf("GC = %d, limit = %d", gc, limit)
			}
			g.restore()
			if gc, limit := gcSettings(); gc != 100 || limit != 16<<30 {
				t.Fatalf("restored GC = %d, limit = %d", gc, limit)
			}
		})
	}
}

func TestBatchGCDefersFirstCollection(t *testing.T) {
	setGCEnv(t, nil, nil)
	g := configureBatchGC(8 << 30)
	defer g.restore()
	if gc, limit := gcSettings(); gc != -1 || limit != 6<<30 {
		t.Fatalf("before binding: GC = %d, limit = %d", gc, limit)
	}
	g.programBound()
	// The test binary's heap is far below the soft limit, so the limit drops to 4x the heap.
	if gc, limit := gcSettings(); gc != -1 || limit <= 0 || limit >= 4<<30 {
		t.Fatalf("after binding: GC = %d, limit = %d", gc, limit)
	}
	runtime.GC()
	deadline := time.Now().Add(10 * time.Second)
	for {
		gc, limit := gcSettings()
		if gc == 300 && limit == 4<<30 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after first collection: GC = %d, limit = %d", gc, limit)
		}
		time.Sleep(time.Millisecond)
	}
	// Later programs no longer change the limit.
	g.programBound()
	if gc, limit := gcSettings(); gc != 300 || limit != 4<<30 {
		t.Fatalf("after switch: GC = %d, limit = %d", gc, limit)
	}
}

func TestBatchMemoryLimit(t *testing.T) {
	for _, test := range []struct {
		name string
		live uint64
		want int64
	}{
		{name: "small live heap keeps the soft limit", live: 1 << 30, want: 4 << 30},
		{name: "half the soft limit", live: 2 << 30, want: 4 << 30},
		{name: "large live heap gets the default goal", live: 3 << 30, want: 6 << 30},
		{name: "huge live heap does not overflow", live: math.MaxUint64, want: math.MaxInt64 - 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := batchMemoryLimit(4<<30, test.live); got != test.want {
				t.Fatalf("batchMemoryLimit(4GiB, %d) = %d, want %d", test.live, got, test.want)
			}
		})
	}
}

func TestBatchGCRestoreStopsAdjusting(t *testing.T) {
	setGCEnv(t, nil, nil)
	g := configureBatchGC(8 << 30)
	g.restore()
	// A collection reported after the compilation must not change the restored settings.
	g.collected()
	if gc, limit := gcSettings(); gc != 100 || limit != 16<<30 {
		t.Fatalf("GC = %d, limit = %d", gc, limit)
	}
}

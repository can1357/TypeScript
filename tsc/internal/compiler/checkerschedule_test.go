package compiler

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
)

// scheduleWorkload models checkers whose work for a file depends on the files they checked
// before, as with checker caches: a file costs half as much on a checker that checked its
// predecessor.
type scheduleWorkload struct {
	associations []int
	weights      []int
	costs        []uint64
	checkerCount int
}

func newScheduleWorkload(seed uint64, fileCount int, checkerCount int) scheduleWorkload {
	rng := rand.New(rand.NewPCG(seed, seed))
	w := scheduleWorkload{
		associations: make([]int, fileCount),
		weights:      make([]int, fileCount),
		costs:        make([]uint64, fileCount),
		checkerCount: checkerCount,
	}
	// About three times the work before the first checkpoint per checker.
	maxCost := 6 * checkerScheduleStart * checkerCount / fileCount
	for i := range fileCount {
		// Skewed associations, and weights unrelated to the costs, as from a static estimate.
		w.associations[i] = min(rng.IntN(checkerCount+2), checkerCount-1)
		w.weights[i] = 1 + rng.IntN(100)
		w.costs[i] = 1 + uint64(rng.IntN(maxCost))
	}
	return w
}

func (w scheduleWorkload) cost(file int, checked []bool) uint64 {
	if file > 0 && checked[file-1] {
		return w.costs[file] / 2
	}
	return w.costs[file]
}

// run checks the files with concurrent checkers that publish their work in steps, one of them
// much slower than the others so that they get many checkpoints ahead of it, and returns the files
// each checker checks in order.
func (w scheduleWorkload) run(seed uint64) [][]int {
	s := newCheckerSchedule(w.associations, w.weights, w.checkerCount)
	order := make([][]int, w.checkerCount)
	slow := int(seed % uint64(w.checkerCount))
	var wg sync.WaitGroup
	for i := range w.checkerCount {
		wg.Go(func() {
			defer s.finish(i)
			rng := rand.New(rand.NewPCG(seed, uint64(i)))
			checked := make([]bool, len(w.costs))
			var work uint64
			for {
				file, ok := s.start(i, work)
				if !ok {
					return
				}
				cost := w.cost(file, checked)
				for step := range 4 {
					work += cost/4 + uint64(step/3)*(cost%4)
					s.publish(i, work)
					if i == slow || rng.IntN(16) == 0 {
						time.Sleep(time.Duration(rng.IntN(20)) * time.Microsecond)
					}
				}
				checked[file] = true
				order[i] = append(order[i], file)
			}
		})
	}
	wg.Wait()
	return order
}

func TestCheckerScheduleIsDeterministic(t *testing.T) {
	t.Parallel()
	for _, checkerCount := range []int{2, 4, 8} {
		w := newScheduleWorkload(uint64(checkerCount), 300, checkerCount)
		want := w.run(0)
		checks := make([]int, len(w.associations))
		moved := 0
		for c, files := range want {
			for _, file := range files {
				checks[file]++
				if w.associations[file] != c {
					moved++
				}
			}
		}
		if i := slices.IndexFunc(checks, func(n int) bool { return n != 1 }); i >= 0 {
			t.Fatalf("%d checkers: file %d was checked %d times", checkerCount, i, checks[i])
		}
		if moved == 0 {
			t.Fatalf("%d checkers: the workload does not exercise moving files", checkerCount)
		}
		for seed := range uint64(10) {
			if got := w.run(seed + 1); !slices.EqualFunc(got, want, slices.Equal) {
				t.Fatalf("%d checkers, seed %d: the files each checker checks depend on timing", checkerCount, seed+1)
			}
		}
	}
}

func TestCheckerScheduleMovesFilesToIdleCheckers(t *testing.T) {
	t.Parallel()
	// All files start on checker 0; checker 1 must receive files beyond the frozen part.
	associations := make([]int, 40)
	weights := slices.Repeat([]int{10}, 40)
	s := newCheckerSchedule(associations, weights, 2)
	var mu sync.Mutex
	checkedBy := make([]int, 40)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			defer s.finish(i)
			var work uint64
			for {
				file, ok := s.start(i, work)
				if !ok {
					return
				}
				work += checkerScheduleStart / 10
				mu.Lock()
				checkedBy[file] = i
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	onSecond := 0
	for _, c := range checkedBy {
		onSecond += c
	}
	if onSecond < 10 {
		t.Fatalf("checker 1 checked %d of 40 files", onSecond)
	}
}

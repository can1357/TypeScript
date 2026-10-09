package checker

import "math"

// The estimated work of a checker (see Checker.Work) is a weighted count of its operations, in
// nanoseconds of a reference machine. Each weight is the average cost of the operation including
// everything it does that the other counted operations do not cover. They were fitted by
// non-negative least squares to the per-file check times of VS Code, Sentry, Playwright, TypeORM
// and Excalidraw with one checker on an x86-64 server. With 4 and 8 checkers on those projects, the
// ratio of real to estimated work of the slowest checker exceeds the average ratio by 9% (14% at
// most); counting only the types, symbols, signatures and instantiations created, by 17% (41%).
const (
	workPerCheckedFile      = 24000
	workPerMemberResolution = 1470
	workPerUnion            = 830
	workPerExpression       = 520
	workPerSymbol           = 250
	workPerRelation         = 150
	workPerType             = 130
	workPerPropertyLookup   = 64
)

// Work returns the estimated work of the checker so far, in nanoseconds of a reference machine.
//
// Unlike the elapsed time, the estimate is a deterministic function of the checker's computation,
// independent of the machine, its load and the scheduling of goroutines. The compiler uses it to
// distribute files across checkers deterministically (see compiler.checkerPool).
func (c *Checker) Work() uint64 {
	return c.work
}

// ObserveWork arranges for observer to be called with the checker's work whenever it has grown by
// interval since the last call, or removes the observer if it is nil. The observer runs on the
// goroutine using the checker, in the middle of its operations: it must not use the checker.
func (c *Checker) ObserveWork(interval uint64, observer func(work uint64)) {
	c.workObserver = observer
	c.workInterval = interval
	c.nextWorkReport = math.MaxUint64
	if observer != nil {
		c.nextWorkReport = c.work + interval
	}
}

func (c *Checker) addWork(units uint64) {
	c.work += units
	if c.work >= c.nextWorkReport {
		c.reportWork()
	}
}

// reportWork is kept out of line so that addWork, on every hot path that counts work, inlines.
//
//go:noinline
func (c *Checker) reportWork() {
	if c.workObserver == nil {
		// Without an observer, stop reporting. (A checker that does not come from NewChecker
		// starts with a zero threshold.)
		c.nextWorkReport = math.MaxUint64
		return
	}
	c.nextWorkReport = c.work + c.workInterval
	c.workObserver(c.work)
}

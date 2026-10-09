package compiler

import (
	"math"
	"sync"
	"sync/atomic"
)

/*
A checking pass distributes the files over the checkers, starting from the partition (see
checkerPool.createCheckers): each checker checks a queue of files, initially those associated with
it, in program order. The partition can only estimate the work of a file from its syntax, and the
slowest checker regularly took up to a quarter longer than the average. So at regular checkpoints,
the schedule moves files from the end of the queues with the most remaining estimated work to the
end of the queues with the least. A checker that falls behind, because its files turned out to be
expensive, keeps more of its estimated work and so gives files away.

Which checker checks which file must not depend on timing: the caches of a checker depend on the
files it checked, and in rare cases so do diagnostics. The checkpoints are therefore placed in the
deterministic virtual time of checker.Checker.Work: the files are moved as if all checkers did
their work at the same rate, stopping at each checkpoint. In real time, a checker continues past a
checkpoint while the others are still on their way to it, through the files the move cannot take (a
frozen part of its queue); only the decision, made once the last checker has passed the checkpoint,
can let it start a file beyond them. A checker without files left waits for the next checkpoint that
gives it some, at whose virtual time it resumes.

Moving a file costs its new checker the caches the file would have found on its old one, and every
checkpoint makes the checkers that are ahead in real time wait for the others more often. In short
checks, these costs outweighed the gains, so the first checkpoint comes only after
checkerScheduleStart of work; and a checkpoint only moves files while the remaining work of two
checkers differs by more than a quarter of the average.
*/

const (
	// checkerScheduleStart is the work of each checker before the first checkpoint, and
	// checkerScheduleInterval the work between checkpoints, in nanoseconds of a reference machine
	// (see checker.Checker.Work).
	checkerScheduleStart    = 200_000_000
	checkerScheduleInterval = 1_000_000

	// checkerWorkPublishInterval is the work after which a checker publishes its work again.
	checkerWorkPublishInterval = 20_000

	// A checkpoint does not move the next quarter (and at least the next checkerFrozenFiles) of
	// the files a checker has yet to start, so that checkers ahead of others need not wait for
	// the decision.
	checkerFrozenShareDivisor = 4
	checkerFrozenFiles        = 4

	// A checkpoint moves files only while the remaining estimated work of two checkers differs
	// by more than 1/checkerImbalanceDivisor of the average.
	checkerImbalanceDivisor = 4
)

// checkerSchedule distributes the files of one checking pass over the checkers; see above.
type checkerSchedule struct {
	weights []int // The estimated work of each file.

	mu      sync.Mutex
	changed sync.Cond // Signaled after each decision.
	// decided is the number of checkpoints decided; see checkpointAt.
	decided  uint64
	finished bool // Set once no checker has files left.
	checkers []scheduledChecker
	// movedTo holds the checker each file was moved to, or -1.
	movedTo []int
}

type scheduledChecker struct {
	// queue holds the files of the checker; the first next of them have been started. queued is
	// the estimated work of all of them, and started that of the started ones.
	queue   []int
	next    int
	queued  int
	started int
	// passed is the number of checkpoints the checker has passed. pending holds its position at
	// each checkpoint it passed that is not yet decided, from checkpoint decided+1.
	passed  uint64
	pending []queuePosition
	// nextCheckpoint is the work (without offset) at which the checker passes its next
	// checkpoint, read by publish without the lock.
	nextCheckpoint atomic.Uint64
	// offset is the virtual time the checker spent without files: its virtual time is its work
	// plus offset. Only the checker changes it, with mu held.
	offset uint64
	// idle is set while the checker has no files left, since virtual time idleAt.
	idle   bool
	idleAt uint64
	// frozen is the index in queue below which decisions have frozen the files. It never
	// decreases, so the checker may start any file below it.
	frozen int
}

// queuePosition is the progress of a checker through its queue when it passed a checkpoint.
type queuePosition struct {
	next    int // The number of files it had started.
	started int // Their estimated work.
}

// checkpointAt returns the virtual time of checkpoint k, from 1.
func checkpointAt(k uint64) uint64 {
	return checkerScheduleStart + (k-1)*checkerScheduleInterval
}

// position returns the progress of the checker at the earliest checkpoint it passed that is not
// decided, or its current progress if there is none: a checker without one has finished.
func (c *scheduledChecker) position() queuePosition {
	if len(c.pending) != 0 {
		return c.pending[0]
	}
	return queuePosition{c.next, c.started}
}

// frozenBoundary returns the index in the queue below which the decision of a checkpoint the
// checker passed at position does not move files, if no decision changes the queue before.
func (c *scheduledChecker) frozenBoundary(position queuePosition) int {
	unstarted := len(c.queue) - position.next
	return max(c.frozen, position.next+max(unstarted/checkerFrozenShareDivisor, checkerFrozenFiles))
}

// newCheckerSchedule distributes files, given as the index of their associated checker in
// associations and their estimated work in weights, over checkerCount checkers.
func newCheckerSchedule(associations []int, weights []int, checkerCount int) *checkerSchedule {
	s := &checkerSchedule{
		weights:  weights,
		checkers: make([]scheduledChecker, checkerCount),
		movedTo:  make([]int, len(associations)),
	}
	s.changed.L = &s.mu
	for file, checkerIndex := range associations {
		c := &s.checkers[checkerIndex]
		c.queue = append(c.queue, file)
		c.queued += weights[file]
		s.movedTo[file] = -1
	}
	for i := range s.checkers {
		s.checkers[i].nextCheckpoint.Store(checkpointAt(1))
	}
	return s
}

// publish records the work of checker checkerIndex while it checks a file.
func (s *checkerSchedule) publish(checkerIndex int, work uint64) {
	if work < s.checkers[checkerIndex].nextCheckpoint.Load() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pass(checkerIndex, work+s.checkers[checkerIndex].offset)
}

// start returns the next file for checker checkerIndex to check at work, or false once no checker
// has files left. It waits while a pending decision may move the file, or while the checker has
// no files.
func (s *checkerSchedule) start(checkerIndex int, work uint64) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &s.checkers[checkerIndex]
	s.pass(checkerIndex, work+c.offset)
	for {
		if c.idle && c.next < len(c.queue) {
			// A decision gave files to the idle checker. If it passed that checkpoint while it
			// still had files, they continue its queue; otherwise it starts them at the checkpoint,
			// the last it passed.
			c.idle = false
			c.offset = max(c.idleAt, checkpointAt(c.passed)) - work
			c.nextCheckpoint.Store(checkpointAt(c.passed+1) - c.offset)
		}
		if c.next < len(c.queue) {
			if s.mayStart(c) {
				file := c.queue[c.next]
				c.next++
				c.started += s.weights[file]
				return file, true
			}
		} else if !c.idle {
			c.idle = true
			c.idleAt = work + c.offset
			s.passIdle(checkerIndex)
			// Passing may have decided a checkpoint that gave the checker files.
			continue
		}
		if s.finished {
			return 0, false
		}
		s.changed.Wait()
	}
}

// mayStart reports whether checker c may start its next file: it must not be in the part of its
// queue that a pending decision can move. Called with mu held.
func (s *checkerSchedule) mayStart(c *scheduledChecker) bool {
	if len(c.pending) == 0 {
		// The file will count as started at the next checkpoint.
		return true
	}
	// The queue cannot change before the earliest pending decision, and frozen boundaries never
	// decrease, so no later decision moves the files below its boundary either.
	return c.next < c.frozenBoundary(c.pending[0])
}

// pass records that checker checkerIndex has reached virtual time at. Called with mu held.
func (s *checkerSchedule) pass(checkerIndex int, at uint64) {
	c := &s.checkers[checkerIndex]
	for checkpointAt(c.passed+1) <= at {
		c.passed++
		c.pending = append(c.pending, queuePosition{c.next, c.started})
	}
	c.nextCheckpoint.Store(checkpointAt(c.passed+1) - c.offset)
	s.decide()
}

// passIdle records that idle checker checkerIndex passes the next undecided checkpoint, as it
// starts nothing before. Called with mu held.
func (s *checkerSchedule) passIdle(checkerIndex int) {
	c := &s.checkers[checkerIndex]
	if c.passed == s.decided {
		c.passed++
		c.pending = append(c.pending, queuePosition{c.next, c.started})
	}
	s.decide()
}

// decide makes the decisions of all checkpoints that every checker has passed. Called with mu held.
func (s *checkerSchedule) decide() {
	for {
		checkpoint := s.decided + 1
		allIdle := true
		for i := range s.checkers {
			c := &s.checkers[i]
			if c.passed < checkpoint {
				return
			}
			allIdle = allIdle && c.idle
		}
		s.rebalance()
		s.decided = checkpoint
		for i := range s.checkers {
			c := &s.checkers[i]
			if len(c.pending) != 0 {
				c.pending = c.pending[1:]
			}
			if c.idle && c.next == len(c.queue) && c.passed == s.decided {
				// Without files from this decision, the checker stays idle until the next one.
				c.passed++
				c.pending = append(c.pending, queuePosition{c.next, c.started})
			}
		}
		if allIdle && s.noUnstartedFiles() {
			s.finished = true
		}
		s.changed.Broadcast()
		if s.finished {
			return
		}
	}
}

func (s *checkerSchedule) noUnstartedFiles() bool {
	for i := range s.checkers {
		if s.checkers[i].next < len(s.checkers[i].queue) {
			return false
		}
	}
	return true
}

// rebalance moves files beyond the frozen parts of the queues at the checkpoint being decided, one
// at a time, from the end of the queue with the most remaining estimated work to the end of the
// queue with the least, while their difference exceeds the imbalance threshold and the move
// reduces it. Called with mu held.
func (s *checkerSchedule) rebalance() {
	remaining := make([]int, len(s.checkers))
	total := 0
	for i := range s.checkers {
		c := &s.checkers[i]
		position := c.position()
		c.frozen = c.frozenBoundary(position)
		remaining[i] = c.queued - position.started
		total += remaining[i]
	}
	threshold := total / (len(s.checkers) * checkerImbalanceDivisor)
	for {
		// The donor is the checker with the most remaining work that can give files.
		donor, receiver := -1, 0
		for i := range remaining {
			if c := &s.checkers[i]; len(c.queue) > c.frozen && (donor < 0 || remaining[i] > remaining[donor]) {
				donor = i
			}
			if remaining[i] < remaining[receiver] {
				receiver = i
			}
		}
		if donor < 0 {
			return
		}
		from := &s.checkers[donor]
		file := from.queue[len(from.queue)-1]
		weight := s.weights[file]
		if difference := remaining[donor] - remaining[receiver]; difference <= max(threshold, weight) {
			return
		}
		to := &s.checkers[receiver]
		from.queue = from.queue[:len(from.queue)-1]
		to.queue = append(to.queue, file)
		from.queued -= weight
		to.queued += weight
		remaining[donor] -= weight
		remaining[receiver] += weight
		s.movedTo[file] = receiver
	}
}

// finish records that checker checkerIndex starts no more files, even if it failed.
func (s *checkerSchedule) finish(checkerIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &s.checkers[checkerIndex]
	// Its unstarted files cannot be checked anymore; make it pass every checkpoint.
	c.queue = c.queue[:c.next]
	c.queued = c.started
	c.idle = true
	c.pending = nil
	c.passed = math.MaxUint64
	c.nextCheckpoint.Store(math.MaxUint64)
	s.decide()
	s.changed.Broadcast()
}

package compiler

import (
	"context"
	"maps"
	"math"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/checker"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/tracing"
)

// CheckerPool is implemented by the project system to provide checkers with
// request-scoped lifetime and reclamation. It returns a checker and a release
// function that must be called when the caller is done with the checker.
// The returned checker must not be accessed concurrently; each acquisition is exclusive.
// Acquisitions are not reentrant, even when they share a request ID. Callers must
// pass an already acquired checker to nested operations instead of acquiring again.
// If file is non-nil, the pool may use it as an affinity hint to return the same
// checker for the same file across calls.
type CheckerPool interface {
	GetChecker(ctx context.Context, file *ast.SourceFile) (*checker.Checker, func())
}

type checkerPool struct {
	program *Program
	tracing *tracing.Tracing

	createCheckersOnce sync.Once
	checkers           []*checker.Checker
	locks              []*sync.Mutex
	// fileAssociations holds the checker of each file. The first checking pass with several
	// checkers replaces it with the checkers it ended up using (see forEachCheckerScheduleDo), so
	// that later passes find the caches it built; lookups load it once.
	fileAssociations atomic.Pointer[checkerAssociations]
	// partitionOnce guards associations, the checker index of each program file, and fileWeights,
	// the estimated work of each file for scheduling (see partitionFiles and checkerSchedule).
	partitionOnce sync.Once
	associations  []int
	fileWeights   map[*ast.SourceFile]int
	// scheduled is set once a checking pass has distributed files dynamically.
	scheduled atomic.Bool
}

// checkerAssociations maps each file of a program to its checker.
type checkerAssociations map[*ast.SourceFile]*checker.Checker

// checkerFor returns the checker associated with file.
func (p *checkerPool) checkerFor(file *ast.SourceFile) *checker.Checker {
	return (*p.fileAssociations.Load())[file]
}

var _ CheckerPool = (*checkerPool)(nil)

/*
Checker association is a balanced graph-partitioning problem:

  - A vertex is a source file.
  - An undirected edge connects two files for each resolved, in-program import
    entry between them. Multiple entries may connect the same pair and therefore
    strengthen their affinity. Self-imports and unresolved or external targets do
    not create edges.
  - A partition is a checker with its own symbol, type, and instantiation caches.

Putting related files on the same checker reduces duplicated cache construction,
but concentrating too many roots on one checker increases the parallel critical
path. We use weighted FENNEL to trade off those objectives:

  affinity(partition) - alpha * incrementalLoadPenalty(partition)

See Tsourakakis et al., "FENNEL: Streaming Graph Partitioning for Massive Scale
Graphs", WSDM 2014: https://doi.org/10.1145/2556195.2556213.

FENNEL is sensitive to stream order. This is both established in the partitioning
literature (for example, Awadelkarim and Ugander, "Prioritized Restreaming
Algorithms for Balanced Graph Partitioning", KDD 2020:
https://arxiv.org/abs/2007.03131) and pronounced in checker workloads because
semantic work is demand-driven. During calibration with four checkers,
degree-first streams produced nearly equal estimated loads but highly unequal
per-checker completion times:

  - MUI docs: approximately 0.6s, 2.8s, 15.3s, and 19.0s.
  - XState: approximately 0.04s, 0.35s, 0.67s, and 1.18s.

Seeded random-order testing also found repeatable slow orders with identical
diagnostics, including about +37% MUI docs and +59% XState compiler Check time
relative to normal order, again with four checkers. Therefore stream order is part
of the policy below, rather than an incidental implementation detail.
*/

/*
The constants below are empirical safety factors for a work proxy that cannot
observe future semantic cache construction. They were swept across representative
projects including VS Code, TypeScript, MUI docs, XState, and Bluesky, with 2, 4,
and 8 checkers:

  - 100-byte text weight divisor: among 25, 50, 75, 80, 90, 100, 110, 125, 150,
    200, and 400, this kept large literals, comments, and generated files from
    appearing artificially cheap without allowing raw byte length to dominate.
    Nearby values occasionally improved one project, but 100 was the most robust
    setting, particularly on VS Code and MUI docs.
  - 4x source-file weight: among 2x, 3x, 4x, 5x, and 8x, this best balanced
    declaration-heavy projects without losing the locality benefit.
  - 16x FENNEL penalty: 1x, 2x, 4x, 8x, 12x, 16x, 20x, 24x, 32x, and 64x were
    sampled across the experiments; 16x was the most robust balance/locality
    compromise for declaration-heavy projects.
  - 12x prioritized-source penalty: 8x, 12x, 16x, 20x, and 24x were compared;
    source-first ordering already spreads expensive roots, and 12x retained more
    locality than the stronger settings.
  - 4-checker cutoff: at 2-3 checkers the tight load cap provides enough balance;
    extra source weighting and penalty pressure regressed some projects.
  - 3 streaming passes: among 1, 3, 4, 6, 9, and 17 passes on VS Code, Sentry,
    Playwright, TypeORM, Excalidraw, and tRPC with 4 and 8 checkers, 3 passes cut
    the summed checker time by up to 21% (Sentry, 8 checkers) and the slowest
    checker by up to 14%; more passes oscillated rather than converged.

These are project-independent operating points, not formulas derived by FENNEL.
Rebenchmark the vscode, self-compiler, mui-docs, and xstate-main scenarios in the
TypeScript-benchmarking repository at 2, 4, and 8 checkers before changing them.
*/
const (
	checkerAssociationTextWeightDivisor            = 100
	checkerAssociationSourceFileWeightMultiplier   = 4
	checkerAssociationBalancePenaltyMultiplier     = 16
	checkerAssociationPrioritizedSourcePenalty     = 12
	checkerAssociationStrongBalanceMinCheckerCount = 4
	checkerAssociationPasses                       = 3
)

type checkerAssociationPolicy struct {
	prioritizeSourceFiles      bool
	sourceFileWeightMultiplier int
	balancePenaltyMultiplier   int
}

/*
getCheckerAssociationPolicy selects one of three calibrated regimes:

 1. Source-dominated, any checker count:
    source files first by descending weight; unmodified source-file weight;
    checkerAssociationPrioritizedSourcePenalty.
 2. Declaration-heavy, at least checkerAssociationStrongBalanceMinCheckerCount:
    program order; checkerAssociationSourceFileWeightMultiplier;
    checkerAssociationBalancePenaltyMultiplier.
 3. Declaration-heavy, fewer checkers:
    program order; unmodified source-file weight; unscaled adapted FENNEL
    penalty.

The source-dominated test is evaluated first intentionally: projects with very
little declaration work benefit from balancing source-file roots directly even
with a small checker pool.
*/
func getCheckerAssociationPolicy(totalWeight int, declarationWeight int, checkerCount int) checkerAssociationPolicy {
	if shouldPrioritizeSourceFiles(totalWeight, declarationWeight, checkerCount) {
		return checkerAssociationPolicy{
			prioritizeSourceFiles:      true,
			sourceFileWeightMultiplier: 1,
			balancePenaltyMultiplier:   checkerAssociationPrioritizedSourcePenalty,
		}
	}
	if checkerCount >= checkerAssociationStrongBalanceMinCheckerCount {
		return checkerAssociationPolicy{
			sourceFileWeightMultiplier: checkerAssociationSourceFileWeightMultiplier,
			balancePenaltyMultiplier:   checkerAssociationBalancePenaltyMultiplier,
		}
	}
	return checkerAssociationPolicy{
		sourceFileWeightMultiplier: 1,
		balancePenaltyMultiplier:   1,
	}
}

// getCheckerAssociationsInOrder partitions the import graph using a weighted adaptation
// of FENNEL's streaming graph-partitioning objective with gamma = 3/2. Each file
// is placed where it has the most already-placed neighbors, minus the incremental
// convex load penalty. The published alpha = m*sqrt(k)/n^(3/2) becomes
// m*sqrt(k)/W^(3/2), where W is total estimated checker work. penaltyMultiplier
// applies the empirical safety factor selected by getCheckerAssociationPolicy.
//
// A nil order means stable program order. The preferred maximum checker weight is
// the larger of the largest file and roughly 101% of average. If no checker can
// accept a file under that bound, the file is assigned to the least-loaded checker.
// The 1% slack permits discrete files to pack near the average while preventing
// affinity from deliberately creating meaningful estimated imbalance. Ties are
// deterministic.
//
// A single streaming pass places early files before most of their neighbors, so
// it sees only part of each file's affinity. The partition is therefore restreamed
// (Nishimura and Ugander, "Restreaming Graph Partitioning: Simple Versatile
// Algorithms for Advanced Balancing", KDD 2013:
// https://jugander.github.io/papers/kdd13-restream.pdf): each further pass starts from empty
// loads and streams the files in the same order, but counts a neighbor that is not
// yet placed in this pass at its checker from the previous pass.
func getCheckerAssociationsInOrder(fileWeights []int, adjacentFiles [][]int, fileOrder []int, checkerCount int, penaltyMultiplier int) []int {
	if len(fileWeights) == 0 {
		return nil
	}

	totalWeight := 0
	maxFileWeight := 0
	edgeCount := 0
	for i, weight := range fileWeights {
		totalWeight += weight
		maxFileWeight = max(maxFileWeight, weight)
		edgeCount += len(adjacentFiles[i])
	}

	associations := make([]int, len(fileWeights))
	for i := range associations {
		associations[i] = -1
	}
	checkerWeights := make([]int, checkerCount)
	averageCheckerWeight := (totalWeight + checkerCount - 1) / checkerCount
	maxCheckerWeight := max(maxFileWeight, averageCheckerWeight+averageCheckerWeight/100)
	totalWeightFloat := float64(totalWeight)
	alpha := float64(penaltyMultiplier) * float64(edgeCount/2) * math.Sqrt(float64(checkerCount)) / (totalWeightFloat * math.Sqrt(totalWeightFloat))
	neighborCounts := make([]int, checkerCount)
	var previous []int

	for pass := range checkerAssociationPasses {
		if pass > 0 {
			previous = slices.Clone(associations)
			for i := range associations {
				associations[i] = -1
			}
			clear(checkerWeights)
		}
		streamCheckerAssociations(fileWeights, adjacentFiles, fileOrder, previous, associations, checkerWeights, neighborCounts, maxCheckerWeight, alpha)
	}
	return associations
}

// streamCheckerAssociations runs one FENNEL pass of getCheckerAssociationsInOrder, placing every file
// in associations and adding its weight to checkerWeights. A non-nil previous holds the checker of
// each file from the preceding pass, which stands in for neighbors not yet placed in this pass.
func streamCheckerAssociations(fileWeights []int, adjacentFiles [][]int, fileOrder []int, previous []int, associations []int, checkerWeights []int, neighborCounts []int, maxCheckerWeight int, alpha float64) {
	for position := range fileWeights {
		fileIndex := position
		if fileOrder != nil {
			fileIndex = fileOrder[position]
		}

		clear(neighborCounts)
		for _, adjacentFile := range adjacentFiles[fileIndex] {
			if checkerIndex := associations[adjacentFile]; checkerIndex >= 0 {
				neighborCounts[checkerIndex]++
			} else if previous != nil {
				neighborCounts[previous[adjacentFile]]++
			}
		}

		bestChecker := -1
		bestScore := math.Inf(-1)
		for checkerIndex, checkerWeight := range checkerWeights {
			if checkerWeight+fileWeights[fileIndex] > maxCheckerWeight {
				continue
			}
			oldWeight := float64(checkerWeight)
			newWeight := float64(checkerWeight + fileWeights[fileIndex])
			newPenalty := float64(newWeight * math.Sqrt(newWeight))
			oldPenalty := float64(oldWeight * math.Sqrt(oldWeight))
			penalty := float64(alpha * (newPenalty - oldPenalty))
			score := float64(neighborCounts[checkerIndex]) - penalty
			if score > bestScore || score == bestScore && (bestChecker < 0 || checkerWeight < checkerWeights[bestChecker]) {
				bestChecker = checkerIndex
				bestScore = score
			}
		}
		if bestChecker < 0 {
			bestChecker = 0
			for checkerIndex, checkerWeight := range checkerWeights[1:] {
				if checkerWeight < checkerWeights[bestChecker] {
					bestChecker = checkerIndex + 1
				}
			}
		}
		associations[fileIndex] = bestChecker
		checkerWeights[bestChecker] += fileWeights[fileIndex]
	}
}

// getCheckerAssociationOrder places source files before declarations and
// orders each group by descending estimated work. This exposes expensive semantic
// roots early, when all checker loads are still available. Returning nil preserves
// program order without allocating an index array. Program order is itself a
// locality choice: it preserves deterministic groups produced during program
// construction and was consistently safer for declaration-heavy projects.
func getCheckerAssociationOrder(fileWeights []int, isDeclarationFile []bool, prioritizeSourceFiles bool) []int {
	if !prioritizeSourceFiles {
		return nil
	}
	fileOrder := make([]int, len(fileWeights))
	for i := range fileOrder {
		fileOrder[i] = i
	}
	sort.Slice(fileOrder, func(i, j int) bool {
		left := fileOrder[i]
		right := fileOrder[j]
		if isDeclarationFile[left] != isDeclarationFile[right] {
			return !isDeclarationFile[left]
		}
		if fileWeights[left] != fileWeights[right] {
			return fileWeights[left] > fileWeights[right]
		}
		return left < right
	})
	return fileOrder
}

// Skipped files still participate in import affinity, but are not semantic roots.
// Their demand-driven work belongs to the checked files that reference them.
// Keep a positive weight so even an entirely skipped program has a valid load cap.
func getCheckerAssociationBaseWeight(nodeCount int, textLength int, skipTypeChecking bool) int {
	if skipTypeChecking {
		return 1
	}
	return max(nodeCount+textLength/checkerAssociationTextWeightDivisor, 1)
}

// shouldPrioritizeSourceFiles reports whether all declaration-file base work is at
// most half of one average checker load:
//
//	declarationWeight <= totalWeight / (2 * checkerCount)
//
// This threshold separated source-dominated projects such as VS Code from projects
// where declaration locality remained important, such as MUI docs, TypeScript, and
// XState. Delaying at most half a checker-load of declarations was the stable
// boundary in the cross-project sweeps.
func shouldPrioritizeSourceFiles(totalWeight int, declarationWeight int, checkerCount int) bool {
	return declarationWeight*checkerCount*2 <= totalWeight
}

// getCheckerAssociationWeights combines local syntax work with syntactic import
// fanout from checked roots; skipped files contribute no import work. One import
// unit is totalBaseWeight / totalImports, so imports collectively
// contribute approximately the same vertex weight as syntax. Syntactic imports are
// deliberately broader than getImportAdjacency's resolved, in-program edges: this
// term estimates the work of processing module references, while adjacency controls
// checker affinity. Normalizing the term avoids a project-specific vertex-weight
// constant.
func getCheckerAssociationWeights(baseWeights []int, importCounts []int) []int {
	totalBaseWeight := 0
	totalImports := 0
	for i, baseWeight := range baseWeights {
		totalBaseWeight += baseWeight
		totalImports += importCounts[i]
	}
	importWeight := 0
	if totalImports > 0 {
		importWeight = max(totalBaseWeight/totalImports, 1)
	}
	fileWeights := make([]int, len(baseWeights))
	for i, baseWeight := range baseWeights {
		fileWeights[i] = baseWeight + importCounts[i]*importWeight
	}
	return fileWeights
}

func newCheckerPool(program *Program) *checkerPool {
	return newCheckerPoolWithTracing(program, nil)
}

func newCheckerPoolWithTracing(program *Program, tr *tracing.Tracing) *checkerPool {
	checkerCount := 4
	if program.SingleThreaded() {
		checkerCount = 1
	} else if c := program.Options().Checkers; c != nil {
		checkerCount = *c
	}

	checkerCount = max(min(checkerCount, len(program.files), 256), 1)

	pool := &checkerPool{
		program:  program,
		checkers: make([]*checker.Checker, checkerCount),
		locks:    make([]*sync.Mutex, checkerCount),
		tracing:  tr,
	}

	return pool
}

// GetChecker implements CheckerPool. When file is non-nil, returns the checker
// associated with that file; otherwise returns the first checker.
func (p *checkerPool) GetChecker(ctx context.Context, file *ast.SourceFile) (*checker.Checker, func()) {
	if file != nil {
		return p.getCheckerForFileExclusive(ctx, file)
	}
	p.createCheckers()
	c := p.checkers[0]
	p.locks[0].Lock()
	return c, sync.OnceFunc(func() {
		p.locks[0].Unlock()
	})
}

// getCheckerForFileNonExclusive returns the checker for the given file without locking.
// This is only safe when the caller guarantees no concurrent access to the same checker,
// e.g. for read-only operations like obtaining an emit resolver.
func (p *checkerPool) getCheckerForFileNonExclusive(file *ast.SourceFile) (*checker.Checker, func()) {
	p.createCheckers()
	return p.checkerFor(file), noop
}

func (p *checkerPool) getCheckerForFileExclusive(ctx context.Context, file *ast.SourceFile) (*checker.Checker, func()) {
	p.createCheckers()
	c := p.checkerFor(file)
	idx := slices.Index(p.checkers, c)
	p.locks[idx].Lock()
	return c, sync.OnceFunc(func() {
		p.locks[idx].Unlock()
	})
}

// getCheckerNonExclusive returns the first checker without locking.
func (p *checkerPool) getCheckerNonExclusive() (*checker.Checker, func()) {
	p.createCheckers()
	return p.checkers[0], noop
}

func (p *checkerPool) createCheckers() {
	p.createCheckersOnce.Do(func() {
		checkerCount := len(p.checkers)
		wg := core.NewWorkGroup(p.program.SingleThreaded())
		for i := range checkerCount {
			wg.Queue(func() {
				var tracer *checker.Tracer
				if p.tracing != nil {
					tracer = checker.NewTracer(p.tracing, i)
				}
				p.checkers[i], p.locks[i] = checker.NewChecker(p.program, tracer)
			})
		}

		wg.RunAndWait()

		p.partitionFiles()
		fileAssociations := make(checkerAssociations, len(p.program.files))
		for i, file := range p.program.files {
			fileAssociations[file] = p.checkers[p.associations[i]]
		}
		p.fileAssociations.Store(&fileAssociations)
	})
}

// partitionFiles associates each file of the program with a checker (see
// getCheckerAssociationsInOrder). It only reads the files and their imports, so
// Program.BindSourceFiles runs it while it binds the files.
func (p *checkerPool) partitionFiles() {
	p.partitionOnce.Do(func() {
		checkerCount := len(p.checkers)
		p.associations = make([]int, len(p.program.files))
		if checkerCount > 1 {
			baseWeights := make([]int, len(p.program.files))
			importCounts := make([]int, len(p.program.files))
			isDeclarationFile := make([]bool, len(p.program.files))
			totalBaseWeight := 0
			declarationBaseWeight := 0
			for i, file := range p.program.files {
				skipTypeChecking := p.program.SkipTypeChecking(file, false)
				baseWeight := getCheckerAssociationBaseWeight(file.NodeCount, len(file.Text()), skipTypeChecking)
				totalBaseWeight += baseWeight
				if file.IsDeclarationFile {
					declarationBaseWeight += baseWeight
				}
				baseWeights[i] = baseWeight
				if !skipTypeChecking {
					importCounts[i] = len(file.Imports())
				}
				isDeclarationFile[i] = file.IsDeclarationFile
			}
			policy := getCheckerAssociationPolicy(totalBaseWeight, declarationBaseWeight, checkerCount)
			if policy.sourceFileWeightMultiplier != 1 {
				// Apply this before import normalization. The policy intentionally
				// increases both source-file work and the normalized import unit.
				for i, declaration := range isDeclarationFile {
					if !declaration {
						baseWeights[i] *= policy.sourceFileWeightMultiplier
					}
				}
			}
			fileWeights := getCheckerAssociationWeights(baseWeights, importCounts)
			adjacentFiles := p.getImportAdjacency()
			fileOrder := getCheckerAssociationOrder(fileWeights, isDeclarationFile, policy.prioritizeSourceFiles)
			p.associations = getCheckerAssociationsInOrder(fileWeights, adjacentFiles, fileOrder, checkerCount, policy.balancePenaltyMultiplier)
			p.fileWeights = make(map[*ast.SourceFile]int, len(p.program.files))
			for i, file := range p.program.files {
				p.fileWeights[file] = fileWeights[i]
			}
		}
	})
}

// getImportAdjacency returns an undirected import graph represented by file
// index. A directed import from A to B makes both files adjacent because either
// file can benefit from sharing checker caches with the other.
func (p *checkerPool) getImportAdjacency() [][]int {
	fileIndices := make(map[*ast.SourceFile]int, len(p.program.files))
	for i, file := range p.program.files {
		fileIndices[file] = i
	}
	adjacentFiles := make([][]int, len(p.program.files))
	for fileIndex, file := range p.program.files {
		resolvedModules := p.program.resolvedModules[file.PathKey()]
		for _, resolved := range resolvedModules {
			if resolved == nil || !resolved.IsResolved() {
				continue
			}
			importedFile := p.program.GetSourceFileForResolvedModule(resolved)
			importedIndex, ok := fileIndices[importedFile]
			if !ok || importedIndex == fileIndex {
				continue
			}
			adjacentFiles[fileIndex] = append(adjacentFiles[fileIndex], importedIndex)
			adjacentFiles[importedIndex] = append(adjacentFiles[importedIndex], fileIndex)
		}
	}
	return adjacentFiles
}

// Runs `cb` for each checker in the pool concurrently, locking and unlocking checker mutexes as it goes,
// making it safe to call `forEachCheckerParallel` from many threads simultaneously.
func (p *checkerPool) forEachCheckerParallel(cb func(idx int, c *checker.Checker)) {
	p.createCheckers()
	wg := core.NewWorkGroup(p.program.SingleThreaded())
	for idx, checker := range p.checkers {
		wg.Queue(func() {
			p.locks[idx].Lock()
			defer p.locks[idx].Unlock()
			cb(idx, checker)
		})
	}
	wg.RunAndWait()
}

func (p *checkerPool) GetGlobalDiagnostics() []*ast.Diagnostic {
	p.createCheckers()
	globalDiagnostics := make([][]*ast.Diagnostic, len(p.checkers))
	p.forEachCheckerParallel(func(idx int, checker *checker.Checker) {
		globalDiagnostics[idx] = checker.GetGlobalDiagnostics()
	})
	return SortAndDeduplicateDiagnostics(slices.Concat(globalDiagnostics...))
}

// forEachCheckerGroupDo runs one task per checker in parallel. Each task iterates
// the provided files, processing only those assigned to its checker. Within each
// checker's set, files are visited in their original order. The first pass with
// several checkers moves files between the sets as it goes (see checkerSchedule).
func (p *checkerPool) forEachCheckerGroupDo(ctx context.Context, files []*ast.SourceFile, singleThreaded bool, cb func(c *checker.Checker, fileIndex int, file *ast.SourceFile)) {
	p.createCheckers()

	checkerCount := len(p.checkers)
	if !singleThreaded && checkerCount > 1 && p.scheduled.CompareAndSwap(false, true) {
		p.forEachCheckerScheduleDo(files, cb)
		return
	}
	fileAssociations := *p.fileAssociations.Load()
	wg := core.NewWorkGroup(singleThreaded)
	for checkerIdx := range checkerCount {
		wg.Queue(func() {
			p.locks[checkerIdx].Lock()
			defer p.locks[checkerIdx].Unlock()
			for i, file := range files {
				if checker := p.checkers[checkerIdx]; checker == fileAssociations[file] {
					cb(checker, i, file)
				}
			}
		})
	}
	wg.RunAndWait()
}

// forEachCheckerFileDo runs one task per checker in parallel, which calls cb for each of files
// associated with its checker, in order. Unlike forEachCheckerGroupDo, the task does not hold the
// checker's lock: cb must lock it for each use, as emit resolvers do.
func (p *checkerPool) forEachCheckerFileDo(files []*ast.SourceFile, singleThreaded bool, cb func(fileIndex int, file *ast.SourceFile)) {
	p.createCheckers()
	fileAssociations := *p.fileAssociations.Load()
	wg := core.NewWorkGroup(singleThreaded)
	for _, c := range p.checkers {
		wg.Queue(func() {
			for i, file := range files {
				if fileAssociations[file] == c {
					cb(i, file)
				}
			}
		})
	}
	wg.RunAndWait()
}

// forEachCheckerScheduleDo runs cb for each file on the checker a checkerSchedule assigns it to, and
// associates the files with those checkers.
func (p *checkerPool) forEachCheckerScheduleDo(files []*ast.SourceFile, cb func(c *checker.Checker, fileIndex int, file *ast.SourceFile)) {
	checkerIndices := make(map[*checker.Checker]int, len(p.checkers))
	for i, c := range p.checkers {
		checkerIndices[c] = i
	}
	fileAssociations := *p.fileAssociations.Load()
	associations := make([]int, len(files))
	weights := make([]int, len(files))
	for i, file := range files {
		associations[i] = checkerIndices[fileAssociations[file]]
		weights[i] = p.fileWeights[file]
	}
	schedule := newCheckerSchedule(associations, weights, len(p.checkers))
	wg := core.NewWorkGroup(false /*singleThreaded*/)
	for checkerIdx, c := range p.checkers {
		wg.Queue(func() {
			p.locks[checkerIdx].Lock()
			defer p.locks[checkerIdx].Unlock()
			// Finishing also releases checkers waiting for this one if cb panics.
			defer schedule.finish(checkerIdx)
			c.ObserveWork(checkerWorkPublishInterval, func(work uint64) {
				schedule.publish(checkerIdx, work)
			})
			defer c.ObserveWork(0, nil)
			for {
				i, ok := schedule.start(checkerIdx, c.Work())
				if !ok {
					break
				}
				cb(c, i, files[i])
			}
		})
	}
	wg.RunAndWait()
	// Passes running concurrently keep the associations they loaded.
	scheduledAssociations := maps.Clone(fileAssociations)
	for i, checkerIdx := range schedule.movedTo {
		if checkerIdx >= 0 {
			scheduledAssociations[files[i]] = p.checkers[checkerIdx]
		}
	}
	p.fileAssociations.Store(&scheduledAssociations)
}

func noop() {}

package checker

import (
	"sync"
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
)

func TestArenaLinkStoreCheckerIsolation(t *testing.T) {
	t.Parallel()

	var nodes [512]ast.Node
	var symbols [512]ast.Symbol
	var nodeStores [4]nodeArenaLinkStore[int]
	var symbolStores [4]symbolArenaLinkStore[int]
	var wg sync.WaitGroup
	for checker := range nodeStores {
		wg.Go(func() {
			nodeStore := &nodeStores[checker]
			symbolStore := &symbolStores[checker]
			for i := range nodes {
				if nodeStore.Has(&nodes[i]) || nodeStore.TryGet(&nodes[i]) != nil || symbolStore.Has(&symbols[i]) || symbolStore.TryGet(&symbols[i]) != nil {
					t.Error("unaccessed entry present")
				}
				*nodeStore.Get(&nodes[i]) = checker + 1
				*symbolStore.Get(&symbols[i]) = checker + 1
			}
		})
	}
	wg.Wait()
	for checker := range nodeStores {
		for i := range nodes {
			if got := nodeStores[checker].TryGet(&nodes[i]); got == nil || *got != checker+1 {
				t.Fatalf("node %d value not isolated for checker %d", i, checker)
			}
			if got := symbolStores[checker].TryGet(&symbols[i]); got == nil || *got != checker+1 {
				t.Fatalf("symbol %d value not isolated for checker %d", i, checker)
			}
		}
	}
}

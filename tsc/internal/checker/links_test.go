package checker

import (
	"sync"
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
)

func TestLinkStoreCheckerIsolation(t *testing.T) {
	t.Parallel()

	var nodes [512]ast.Node
	var symbols [512]ast.Symbol
	var nodeStores [4]nodeLinkStore[int]
	var symbolStores [4]symbolLinkStore[int]
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

func TestSymbolLinkStorePeekAssignsNoIds(t *testing.T) {
	t.Parallel()

	var store symbolLinkStore[int]
	var symbol ast.Symbol
	if store.Peek(&symbol) != nil || ast.AssignedSymbolId(&symbol) != 0 {
		t.Fatal("Peek found links for or assigned an id to a symbol without one")
	}
	value := store.Get(&symbol)
	*value = 1
	if store.Peek(&symbol) != value {
		t.Fatal("Peek missed the links of a symbol with a paged id")
	}
	var other ast.Symbol
	ast.GetSymbolId(&other) // An id from the global counter, outside the paged range.
	*store.Get(&other) = 2
	if got := store.Peek(&other); got == nil || *got != 2 {
		t.Fatal("Peek missed the links of a symbol with an unpaged id")
	}
}

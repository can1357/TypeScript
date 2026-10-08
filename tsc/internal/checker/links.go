package checker

import (
	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/core"
)

// nodeLinkStore is a links store keyed by node references. Values are stored directly
// in the pages of the store which is suitable for values where sizeof(V) is small.
type nodeLinkStore[V any] struct {
	store core.PagedLinkStore[V]
	ids   *ast.IdAllocator
}

func (s *nodeLinkStore[V]) Get(node *ast.Node) *V {
	return s.store.Get(uint64(s.ids.NodeId(node)))
}

func (s *nodeLinkStore[V]) Has(node *ast.Node) bool {
	return s.store.Has(uint64(s.ids.NodeId(node)))
}

func (s *nodeLinkStore[V]) TryGet(node *ast.Node) *V {
	return s.store.TryGet(uint64(s.ids.NodeId(node)))
}

// symbolLinkStore is a links store keyed by symbols whose values are stored directly in the
// pages of the store, which suits values where sizeof(V) is small.
type symbolLinkStore[V any] struct {
	store core.PagedLinkStore[V]
	ids   *ast.IdAllocator
}

func (s *symbolLinkStore[V]) Get(symbol *ast.Symbol) *V {
	return s.store.Get(uint64(s.ids.SymbolId(symbol)))
}

// TryGet returns the links of symbol, or nil if it has none. It does not assign an id to the
// symbol: a symbol without an id has no links.
func (s *symbolLinkStore[V]) TryGet(symbol *ast.Symbol) *V {
	if id := ast.AssignedSymbolId(symbol); id != 0 {
		return s.store.TryGet(uint64(id))
	}
	return nil
}

// Arena link stores retain exact entry membership and allocate values only for
// accessed IDs. Each checker owns its pages and arena; only the atomically
// assigned node and symbol IDs are shared between checkers.
type nodeArenaLinkStore[V any] struct {
	store core.PagedArenaLinkStore[V]
	ids   *ast.IdAllocator
}

func (s *nodeArenaLinkStore[V]) Get(node *ast.Node) *V {
	return s.store.Get(uint64(s.ids.NodeId(node)))
}

func (s *nodeArenaLinkStore[V]) Has(node *ast.Node) bool {
	return s.store.Has(uint64(s.ids.NodeId(node)))
}

func (s *nodeArenaLinkStore[V]) TryGet(node *ast.Node) *V {
	return s.store.TryGet(uint64(s.ids.NodeId(node)))
}

// symbolArenaLinkStore stores symbol links indirectly in an arena.
type symbolArenaLinkStore[V any] struct {
	store core.PagedArenaLinkStore[V]
	ids   *ast.IdAllocator
}

func (s *symbolArenaLinkStore[V]) Get(symbol *ast.Symbol) *V {
	return s.store.Get(uint64(s.ids.SymbolId(symbol)))
}

func (s *symbolArenaLinkStore[V]) Has(symbol *ast.Symbol) bool {
	return s.store.Has(uint64(s.ids.SymbolId(symbol)))
}

func (s *symbolArenaLinkStore[V]) TryGet(symbol *ast.Symbol) *V {
	return s.store.TryGet(uint64(s.ids.SymbolId(symbol)))
}

package ast

import "testing"

func TestIdAllocator(t *testing.T) {
	t.Parallel()
	var a, b IdAllocator
	nodes := make([]Node, 3)
	first := a.NodeId(&nodes[0])
	if second := a.NodeId(&nodes[1]); second != first+1 {
		t.Fatalf("ids of one allocator are not consecutive: %d, %d", first, second)
	}
	other := b.NodeId(&nodes[2])
	if other >= first && other < first+minIdBlockSize {
		t.Fatalf("allocators share a block: %d in [%d, %d)", other, first, first+minIdBlockSize)
	}
	if again := a.NodeId(&nodes[2]); again != other {
		t.Fatalf("allocator replaced an assigned id: %d, want %d", again, other)
	}
	// Ids keep increasing across block refills of growing size.
	var c IdAllocator
	many := make([]Node, 5000)
	previous := c.NodeId(&many[0])
	for i := 1; i < len(many); i++ {
		id := c.NodeId(&many[i])
		if id <= previous {
			t.Fatalf("ids decreased across blocks: %d after %d", id, previous)
		}
		previous = id
	}
	var none *IdAllocator
	if none.SymbolId(&Symbol{}) == 0 {
		t.Fatal("nil allocator assigned no id")
	}
}

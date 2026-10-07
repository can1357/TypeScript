package core

import "testing"

func TestPagedArenaLinkStoreOverflow(t *testing.T) {
	var store PagedArenaLinkStore[int]
	value := store.Get(7)
	*value = 42
	store.count = ^uint32(0)
	if store.Get(7) != value || *value != 42 {
		t.Fatal("existing entry changed at maximum count")
	}
	defer func() {
		if got := recover(); got != "PagedArenaLinkStore exceeds 4294967295 live entries" {
			t.Fatalf("unexpected overflow panic: %v", got)
		}
	}()
	store.Get(8)
}

func TestPagedArenaLinkStore(t *testing.T) {
	t.Parallel()

	var store PagedArenaLinkStore[int]
	keys := []uint64{0, pageSize - 1, pageSize, maxPageCount*pageSize - 1, maxPageCount * pageSize, ^uint64(0)}
	values := make([]*int, len(keys))
	for i, key := range keys {
		if store.Has(key) || store.TryGet(key) != nil {
			t.Fatalf("key %d present before Get", key)
		}
		values[i] = store.Get(key)
		if *values[i] != 0 {
			t.Fatalf("key %d not zero initialized", key)
		}
		*values[i] = i + 1
		if store.Has(key^1) || store.TryGet(key^1) != nil {
			t.Fatalf("unaccessed neighbor of key %d present", key)
		}
	}
	for key := uint64(1); key < 3*arenaLinkBlockSize; key++ {
		store.Get(key)
	}
	for i, key := range keys {
		if !store.Has(key) || store.TryGet(key) != values[i] || store.Get(key) != values[i] || *values[i] != i+1 {
			t.Fatalf("key %d lost its stable value after page and arena growth", key)
		}
	}

	var other PagedArenaLinkStore[int]
	if other.Has(keys[0]) || other.TryGet(keys[0]) != nil || *other.Get(keys[0]) != 0 {
		t.Fatal("independent store shares entries")
	}
}

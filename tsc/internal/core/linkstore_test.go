package core

import "testing"

func TestPagedLinkStoreOverflow(t *testing.T) {
	t.Parallel()

	var store PagedLinkStore[int]
	value := store.Get(7)
	*value = 42
	store.count = ^uint32(0)
	if store.Get(7) != value || *value != 42 {
		t.Fatal("existing entry changed at maximum count")
	}
	defer func() {
		if got := recover(); got != "PagedLinkStore exceeds 4294967295 live entries" {
			t.Fatalf("unexpected overflow panic: %v", got)
		}
	}()
	store.Get(8)
}

func TestPagedLinkStore(t *testing.T) {
	t.Parallel()

	var store PagedLinkStore[int]
	keys := []uint64{0, LinkPageSize - 1, LinkPageSize, 1<<24 - 1}
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
	for key := uint64(1); key < 3*linkBlockSize; key++ {
		store.Get(key)
	}
	for i, key := range keys {
		if !store.Has(key) || store.TryGet(key) != values[i] || store.Get(key) != values[i] || *values[i] != i+1 {
			t.Fatalf("key %d lost its stable value after page and block growth", key)
		}
	}

	var other PagedLinkStore[int]
	if other.Has(keys[0]) || other.TryGet(keys[0]) != nil || *other.Get(keys[0]) != 0 {
		t.Fatal("independent store shares entries")
	}
}

package core

import "slices"

// Links store

type LinkStore[K comparable, V any] struct {
	entries map[K]*V
	arena   Arena[V]
}

func (s *LinkStore[K, V]) Get(key K) *V {
	value := s.entries[key]
	if value != nil {
		return value
	}
	if s.entries == nil {
		s.entries = make(map[K]*V)
	}
	value = s.arena.New()
	s.entries[key] = value
	return value
}

func (s *LinkStore[K, V]) Has(key K) bool {
	_, ok := s.entries[key]
	return ok
}

func (s *LinkStore[K, V]) TryGet(key K) *V {
	return s.entries[key]
}

// PagedArenaLinkStore indexes arena-allocated values by dense IDs. Unlike a
// PagedLinkStore, allocating a page does not make its other entries present.
// Pages contain one-based value offsets rather than pointers, halving their
// size and excluding the sparse index entries from GC pointer scanning.
type PagedArenaLinkStore[V any] struct {
	store  PagedLinkStore[uint32]
	blocks []*[arenaLinkBlockSize]V
	count  uint32
}

// For values whose size is a multiple of eight bytes, blocks occupy exact
// multiples of the runtime's 8KB heap pages, avoiding unused size-class padding.
const (
	arenaLinkBlockShift = 10
	arenaLinkBlockSize  = 1 << arenaLinkBlockShift
	arenaLinkBlockMask  = arenaLinkBlockSize - 1
)

func (s *PagedArenaLinkStore[V]) Get(key uint64) *V {
	link := s.store.Get(key)
	index := *link
	if index == 0 {
		return s.allocate(link)
	}
	index--
	return &s.blocks[index>>arenaLinkBlockShift][index&arenaLinkBlockMask]
}

func (s *PagedArenaLinkStore[V]) allocate(link *uint32) *V {
	if s.count == ^uint32(0) {
		panic("PagedArenaLinkStore exceeds 4294967295 live entries")
	}
	index := s.count
	if index&arenaLinkBlockMask == 0 {
		s.blocks = append(s.blocks, newPrefaulted[[arenaLinkBlockSize]V]())
	}
	s.count++
	*link = s.count
	return &s.blocks[index>>arenaLinkBlockShift][index&arenaLinkBlockMask]
}

func (s *PagedArenaLinkStore[V]) Has(key uint64) bool {
	link := s.store.TryGet(key)
	return link != nil && *link != 0
}

func (s *PagedArenaLinkStore[V]) TryGet(key uint64) *V {
	if link := s.store.TryGet(key); link != nil && *link != 0 {
		index := *link - 1
		return &s.blocks[index>>arenaLinkBlockShift][index&arenaLinkBlockMask]
	}
	return nil
}

const (
	pageShift    = 8
	pageSize     = 1 << pageShift
	pageMask     = pageSize - 1
	maxPageCount = 65536
)

// Pages below maxPageCount are found through a directory of page tables, each covering
// pageTableSize consecutive pages. A store whose keys are spread out (ids assigned long apart,
// as in a long language service session) then allocates tables only around its keys.
const (
	pageTableShift = 9
	pageTableSize  = 1 << pageTableShift
	pageTableMask  = pageTableSize - 1
)

// Implements a sparse-array-like structure for storing elements keyed by dense uint64 keys. Elements are
// stored in fixed-size pages of 256 entries and an index of pages is maintained in a two-level table for
// lower valued page indices and a map for higher valued page indices.
type PagedLinkStore[V any] struct {
	pageMap    map[uint64]*[pageSize]V        // Page map for page indices above maxPageCount
	pageTables []*[pageTableSize]*[pageSize]V // Page tables for page indices below maxPageCount
}

func (s *PagedLinkStore[V]) Get(key uint64) *V {
	var page *[pageSize]V
	pageIndex := key >> pageShift
	if pageIndex < maxPageCount {
		tableIndex := int(pageIndex >> pageTableShift)
		if tableIndex >= len(s.pageTables) {
			s.pageTables = slices.Grow(s.pageTables, tableIndex-len(s.pageTables)+1)[:tableIndex+1]
		}
		table := s.pageTables[tableIndex]
		if table == nil {
			table = newPrefaulted[[pageTableSize]*[pageSize]V]()
			s.pageTables[tableIndex] = table
		}
		page = table[pageIndex&pageTableMask]
		if page == nil {
			page = newPrefaulted[[pageSize]V]()
			table[pageIndex&pageTableMask] = page
		}
	} else {
		page = s.pageMap[pageIndex]
		if page == nil {
			page = newPrefaulted[[pageSize]V]()
			if s.pageMap == nil {
				s.pageMap = make(map[uint64]*[pageSize]V)
			}
			s.pageMap[pageIndex] = page
		}
	}
	return &page[key&pageMask]
}

func (s *PagedLinkStore[V]) Has(key uint64) bool {
	return s.TryGet(key) != nil
}

func (s *PagedLinkStore[V]) TryGet(key uint64) *V {
	var page *[pageSize]V
	pageIndex := key >> pageShift
	if pageIndex < maxPageCount {
		if tableIndex := int(pageIndex >> pageTableShift); tableIndex < len(s.pageTables) {
			if table := s.pageTables[tableIndex]; table != nil {
				page = table[pageIndex&pageTableMask]
			}
		}
	} else {
		page = s.pageMap[pageIndex]
	}
	if page != nil {
		return &page[key&pageMask]
	}
	return nil
}

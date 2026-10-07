package core

import (
	"slices"
)

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

const (
	LinkPageShift = 8
	LinkPageSize  = 1 << LinkPageShift
	LinkPageMask  = LinkPageSize - 1
)

// For values whose size is a multiple of eight bytes, value blocks occupy exact multiples of the runtime's
// 8KB heap pages, avoiding unused size-class padding.
const (
	linkBlockShift = 10
	linkBlockSize  = 1 << linkBlockShift
	linkBlockMask  = linkBlockSize - 1
)

// PagedLinkStore implements a sparse-array-like structure for storing elements keyed by dense uint64 keys.
// Elements are allocated in stable blocks of 1024 entries, element references are stored in fixed-size pages
// of 256 entries, and an index of pages is maintained in a growable list. Pages contain one-based offsets of
// elements rather than pointers, halving their size and excluding them from GC pointer scanning. An entry
// is present only once Get has been called for its key.

type PagedLinkStore[V any] struct {
	pages  []*[LinkPageSize]uint32
	blocks []*[linkBlockSize]V
	count  uint32
}

func (s *PagedLinkStore[V]) Get(key uint64) *V {
	pageIndex := key >> LinkPageShift
	if int(pageIndex) >= len(s.pages) {
		// Grow the length of the list to pageIndex+1
		s.pages = slices.Grow(s.pages, int(pageIndex)-len(s.pages)+1)[:pageIndex+1]
	}
	page := s.pages[pageIndex]
	if page == nil {
		page = new([LinkPageSize]uint32)
		s.pages[pageIndex] = page
	}
	link := &page[key&LinkPageMask]
	if *link == 0 {
		return s.allocate(link)
	}
	return s.at(*link)
}

func (s *PagedLinkStore[V]) allocate(link *uint32) *V {
	if s.count == ^uint32(0) {
		panic("PagedLinkStore exceeds 4294967295 live entries")
	}
	index := s.count
	if index&linkBlockMask == 0 {
		s.blocks = append(s.blocks, new([linkBlockSize]V))
	}
	s.count++
	*link = s.count
	return &s.blocks[index>>linkBlockShift][index&linkBlockMask]
}

// at returns the element at the given one-based offset.
func (s *PagedLinkStore[V]) at(offset uint32) *V {
	index := offset - 1
	return &s.blocks[index>>linkBlockShift][index&linkBlockMask]
}

func (s *PagedLinkStore[V]) Has(key uint64) bool {
	return s.offset(key) != 0
}

func (s *PagedLinkStore[V]) TryGet(key uint64) *V {
	if offset := s.offset(key); offset != 0 {
		return s.at(offset)
	}
	return nil
}

// offset returns the one-based offset of the element for key, or 0 if there is none.
func (s *PagedLinkStore[V]) offset(key uint64) uint32 {
	pageIndex := key >> LinkPageShift
	if int(pageIndex) < len(s.pages) {
		if page := s.pages[pageIndex]; page != nil {
			return page[key&LinkPageMask]
		}
	}
	return 0
}

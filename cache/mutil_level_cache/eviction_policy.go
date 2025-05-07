package mutil_level_cache

import (
	"container/heap"
	"time"
)

// LFU堆实现
type LFUItem[K comparable] struct {
	key   K
	count int
	index int // 在堆中的位置
}

type LFUHeap[K comparable] struct {
	items    []*LFUItem[K]
	indexMap map[K]int // 用于快速定位项在堆中的位置
}

func NewLFUHeap[K comparable]() *LFUHeap[K] {
	return &LFUHeap[K]{
		items:    make([]*LFUItem[K], 0),
		indexMap: make(map[K]int),
	}
}

func (h *LFUHeap[K]) Len() int { return len(h.items) }

func (h *LFUHeap[K]) Less(i, j int) bool {
	return h.items[i].count < h.items[j].count
}

func (h *LFUHeap[K]) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].index = i
	h.items[j].index = j
	h.indexMap[h.items[i].key] = i
	h.indexMap[h.items[j].key] = j
}

func (h *LFUHeap[K]) Push(x interface{}) {
	item := x.(*LFUItem[K])
	item.index = len(h.items)
	h.indexMap[item.key] = item.index
	h.items = append(h.items, item)
}

func (h *LFUHeap[K]) Pop() interface{} {
	n := len(h.items)
	item := h.items[n-1]
	h.items = h.items[:n-1]
	delete(h.indexMap, item.key)
	return item
}

// 更新LFU项的访问计数
func (h *LFUHeap[K]) UpdateCount(key K, newCount int) {
	if index, ok := h.indexMap[key]; ok {
		h.items[index].count = newCount
		heap.Fix(h, index)
	}
}

// 从LFU堆中移除特定项
func (h *LFUHeap[K]) Remove(key K) {
	if index, ok := h.indexMap[key]; ok {
		heap.Remove(h, index)
	}
}

// TTL排序结构
type TTLItem[K comparable] struct {
	key      K
	expireAt time.Time
	index    int
}

type TTLHeap[K comparable] struct {
	items    []*TTLItem[K]
	indexMap map[K]int
}

func NewTTLHeap[K comparable]() *TTLHeap[K] {
	return &TTLHeap[K]{
		items:    make([]*TTLItem[K], 0),
		indexMap: make(map[K]int),
	}
}

func (h *TTLHeap[K]) Len() int { return len(h.items) }

func (h *TTLHeap[K]) Less(i, j int) bool {
	return h.items[i].expireAt.Before(h.items[j].expireAt)
}

func (h *TTLHeap[K]) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].index = i
	h.items[j].index = j
	h.indexMap[h.items[i].key] = i
	h.indexMap[h.items[j].key] = j
}

func (h *TTLHeap[K]) Push(x interface{}) {
	item := x.(*TTLItem[K])
	item.index = len(h.items)
	h.indexMap[item.key] = item.index
	h.items = append(h.items, item)
}

func (h *TTLHeap[K]) Pop() interface{} {
	n := len(h.items)
	item := h.items[n-1]
	h.items = h.items[:n-1]
	delete(h.indexMap, item.key)
	return item
}

func (h *TTLHeap[K]) UpdateExpiry(key K, newExpiry time.Time) {
	if index, ok := h.indexMap[key]; ok {
		h.items[index].expireAt = newExpiry
		heap.Fix(h, index)
	}
}

func (h *TTLHeap[K]) Remove(key K) {
	if index, ok := h.indexMap[key]; ok {
		heap.Remove(h, index)
	}
}

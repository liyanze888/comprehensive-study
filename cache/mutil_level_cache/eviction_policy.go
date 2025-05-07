package mutil_level_cache

import (
	"container/heap"
	"container/list"
	"sync"
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

// TinyLFU计数器
type TinyLFUCounter struct {
	counters    []uint8   // 计数器数组
	size        int       // 计数器大小
	windowSize  int       // 窗口大小
	windowCount int       // 当前窗口计数
	windowStart time.Time // 窗口开始时间
}

// 创建新的TinyLFU计数器
func NewTinyLFUCounter(size int, windowSize int) *TinyLFUCounter {
	return &TinyLFUCounter{
		counters:    make([]uint8, size),
		size:        size,
		windowSize:  windowSize,
		windowCount: 0,
		windowStart: time.Now(),
	}
}

// 增加计数
func (c *TinyLFUCounter) Increment(hash uint32) {
	// 检查是否需要重置窗口
	if time.Since(c.windowStart) > time.Duration(c.windowSize)*time.Second {
		c.resetWindow()
	}

	// 增加窗口计数
	c.windowCount++

	// 如果窗口计数达到阈值，进行衰减
	if c.windowCount >= c.windowSize {
		c.decay()
	}

	// 增加计数器值
	index := hash % uint32(c.size)
	if c.counters[index] < 255 {
		c.counters[index]++
	}
}

// 获取计数
func (c *TinyLFUCounter) Get(hash uint32) uint8 {
	return c.counters[hash%uint32(c.size)]
}

// 衰减所有计数器
func (c *TinyLFUCounter) decay() {
	for i := range c.counters {
		c.counters[i] = c.counters[i] / 2
	}
	c.windowCount = 0
	c.windowStart = time.Now()
}

// 重置窗口
func (c *TinyLFUCounter) resetWindow() {
	c.windowCount = 0
	c.windowStart = time.Now()
}

// 双LRU实现
type DoubleLRU[K comparable] struct {
	recent    *list.List          // 最近访问的项
	frequent  *list.List          // 频繁访问的项
	recentMap map[K]*list.Element // 最近访问项的映射
	freqMap   map[K]*list.Element // 频繁访问项的映射
	maxSize   int                 // 最大容量
}

// 创建新的双LRU
func NewDoubleLRU[K comparable](maxSize int) *DoubleLRU[K] {
	return &DoubleLRU[K]{
		recent:    list.New(),
		frequent:  list.New(),
		recentMap: make(map[K]*list.Element),
		freqMap:   make(map[K]*list.Element),
		maxSize:   maxSize,
	}
}

// 访问项
func (dl *DoubleLRU[K]) Access(key K) {
	// 检查是否在频繁访问列表中
	if elem, ok := dl.freqMap[key]; ok {
		dl.frequent.MoveToFront(elem)
		return
	}

	// 检查是否在最近访问列表中
	if elem, ok := dl.recentMap[key]; ok {
		// 移动到频繁访问列表
		dl.recent.Remove(elem)
		delete(dl.recentMap, key)
		newElem := dl.frequent.PushFront(key)
		dl.freqMap[key] = newElem
		return
	}

	// 新项，添加到最近访问列表
	if dl.recent.Len()+dl.frequent.Len() >= dl.maxSize {
		dl.evict()
	}
	newElem := dl.recent.PushFront(key)
	dl.recentMap[key] = newElem
}

// 淘汰项
func (dl *DoubleLRU[K]) evict() {
	// 如果最近访问列表不为空，从其中淘汰
	if dl.recent.Len() > 0 {
		elem := dl.recent.Back()
		key := elem.Value.(K)
		dl.recent.Remove(elem)
		delete(dl.recentMap, key)
		return
	}

	// 如果频繁访问列表不为空，从其中淘汰
	if dl.frequent.Len() > 0 {
		elem := dl.frequent.Back()
		key := elem.Value.(K)
		dl.frequent.Remove(elem)
		delete(dl.freqMap, key)
	}
}

// 移除项
func (dl *DoubleLRU[K]) Remove(key K) {
	if elem, ok := dl.recentMap[key]; ok {
		dl.recent.Remove(elem)
		delete(dl.recentMap, key)
	}
	if elem, ok := dl.freqMap[key]; ok {
		dl.frequent.Remove(elem)
		delete(dl.freqMap, key)
	}
}

// 获取要淘汰的项
func (dl *DoubleLRU[K]) GetEvictionCandidate() (K, bool) {
	if dl.recent.Len() > 0 {
		elem := dl.recent.Back()
		return elem.Value.(K), true
	}
	if dl.frequent.Len() > 0 {
		elem := dl.frequent.Back()
		return elem.Value.(K), true
	}
	var zero K
	return zero, false
}

// WTinyLFU 加权窗口TinyLFU实现
type WTinyLFU[K comparable] struct {
	windowSize    int                 // 窗口大小
	window        *list.List          // 滑动窗口
	windowItems   map[K]*list.Element // 窗口项映射
	frequency     map[K]int           // 频率统计
	decayFactor   float64             // 衰减因子
	lastDecayTime time.Time           // 上次衰减时间
	mutex         sync.RWMutex        // 互斥锁
}

// NewWTinyLFU 创建新的WTinyLFU实例
func NewWTinyLFU[K comparable](windowSize int) *WTinyLFU[K] {
	return &WTinyLFU[K]{
		windowSize:    windowSize,
		window:        list.New(),
		windowItems:   make(map[K]*list.Element),
		frequency:     make(map[K]int),
		decayFactor:   0.95, // 默认衰减因子
		lastDecayTime: time.Now(),
	}
}

// Access 更新访问信息
func (w *WTinyLFU[K]) Access(key K, count int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// 检查是否需要衰减
	w.checkDecay()

	// 更新频率统计
	w.frequency[key] += count

	// 更新滑动窗口
	if elem, exists := w.windowItems[key]; exists {
		// 如果已存在，移动到窗口前端
		w.window.MoveToFront(elem)
	} else {
		// 如果不存在，添加到窗口前端
		elem := w.window.PushFront(key)
		w.windowItems[key] = elem

		// 如果窗口超出大小限制，移除最旧的项
		if w.window.Len() > w.windowSize {
			oldest := w.window.Back()
			if oldest != nil {
				w.window.Remove(oldest)
				delete(w.windowItems, oldest.Value.(K))
			}
		}
	}
}

// GetEvictionCandidate 获取淘汰候选
func (w *WTinyLFU[K]) GetEvictionCandidate() (K, bool) {
	w.mutex.RLock()
	defer w.mutex.RUnlock()

	var candidate K
	var minFreq = -1
	var found bool

	// 遍历窗口中的项，找到频率最低的项
	for k := range w.windowItems {
		freq := w.frequency[k]
		if minFreq == -1 || freq < minFreq {
			minFreq = freq
			candidate = k
			found = true
		}
	}

	return candidate, found
}

// checkDecay 检查并执行频率衰减
func (w *WTinyLFU[K]) checkDecay() {
	now := time.Now()
	if now.Sub(w.lastDecayTime) > time.Minute {
		// 执行频率衰减
		for k := range w.frequency {
			w.frequency[k] = int(float64(w.frequency[k]) * w.decayFactor)
		}
		w.lastDecayTime = now
	}
}

// Remove 移除项
func (w *WTinyLFU[K]) Remove(key K) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// 从窗口中移除
	if elem, exists := w.windowItems[key]; exists {
		w.window.Remove(elem)
		delete(w.windowItems, key)
	}

	// 从频率统计中移除
	delete(w.frequency, key)
}

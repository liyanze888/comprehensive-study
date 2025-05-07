package yz

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"
)

// EvictionReason 定义缓存项被淘汰的原因
type EvictionReason int

const (
	EvictionReasonExpired  EvictionReason = iota // 因TTL过期而被淘汰
	EvictionReasonCapacity                       // 因容量限制而被淘汰
	EvictionReasonMemory                         // 因内存限制而被淘汰
	EvictionReasonManual                         // 被手动移除
)

// String 返回淘汰原因的字符串表示
func (e EvictionReason) String() string {
	switch e {
	case EvictionReasonExpired:
		return "过期淘汰"
	case EvictionReasonCapacity:
		return "容量淘汰"
	case EvictionReasonMemory:
		return "内存淘汰"
	case EvictionReasonManual:
		return "手动移除"
	default:
		return "未知原因"
	}
}

// EvictionPolicy 淘汰策略类型
type EvictionPolicy int

const (
	LRU    EvictionPolicy = iota // 最近最少使用
	LFU                          // 最不常用
	FIFO                         // 先进先出
	Random                       // 随机淘汰
	TTL                          // 仅基于过期时间
)

// EvictionCallback 当缓存项被淘汰时的回调函数
type EvictionCallback[K comparable, V any] func(key K, value V, reason EvictionReason)

// FallbackFunc 回源函数类型定义
type FallbackFunc[K comparable, V any] func(ctx context.Context, key K) (V, error)

// Metrics 保存缓存的统计信息
type Metrics struct {
	Hits              int64 // 缓存命中次数
	Misses            int64 // 缓存未命中次数
	Evictions         int64 // 淘汰次数
	ExpiredEvictions  int64 // 因过期的淘汰次数
	MemoryEvictions   int64 // 因内存限制的淘汰次数
	CapacityEvictions int64 // 因容量限制的淘汰次数
	ManualEvictions   int64 // 手动移除次数
}

// Config 缓存配置
type Config[K comparable, V any] struct {
	ShardCount      int                        // 分片数量
	MaxItems        int                        // 最大项目数量
	MaxMemoryBytes  int64                      // 最大内存使用量(字节)
	DefaultTTL      time.Duration              // 默认的TTL
	ResetTTLOnHits  int                        // 在多少次访问后
	ResetTTLWindow  time.Duration              // 在多少时间内
	EvictionPolicy  EvictionPolicy             // 淘汰策略
	EvictionHandler EvictionCallback[K, V]     // 淘汰回调
	SizeFunc        func(key K, value V) int64 // 自定义大小计算函数
	Fallbacks       []FallbackFunc[K, V]       // 多层fallback函数
}

// item 缓存项
type item[V any] struct {
	value      V
	size       int64
	expireAt   time.Time
	lastAccess time.Time
	accessFreq int
	createdAt  time.Time
	accessHits int
	lastHitAt  time.Time
}

// shard 缓存分片
type shard[K comparable, V any] struct {
	items     map[K]*item[V]
	mutex     sync.RWMutex
	evictList *list.List          // 用于LRU/FIFO
	evictMap  map[K]*list.Element // 用于LRU/FIFO
	freqMap   map[K]int           // 用于LFU
	keysOrder []K                 // 用于FIFO和Random
	config    *Config[K, V]
	totalSize int64 // 此分片的总内存使用量
	cache     *Cache[K, V]
}

// Cache 主缓存结构
type Cache[K comparable, V any] struct {
	shards    []*shard[K, V]
	config    Config[K, V]
	metrics   Metrics
	syncCh    chan struct{} // 用于分布式同步的通道
	stopCh    chan struct{} // 用于停止后台任务
	fallbacks []FallbackFunc[K, V]
}

// NewCache 创建新的缓存实例
func NewCache[K comparable, V any](config Config[K, V]) *Cache[K, V] {
	if config.ShardCount <= 0 {
		config.ShardCount = 16
	}

	if config.SizeFunc == nil {
		// 默认大小评估函数 (这只是一个粗略估计)
		config.SizeFunc = func(key K, value V) int64 {
			return 64 // 默认大小
		}
	}

	c := &Cache[K, V]{
		shards:    make([]*shard[K, V], config.ShardCount),
		config:    config,
		syncCh:    make(chan struct{}, 100),
		stopCh:    make(chan struct{}),
		fallbacks: config.Fallbacks,
	}

	// 初始化所有分片
	for i := 0; i < config.ShardCount; i++ {
		c.shards[i] = &shard[K, V]{
			items:     make(map[K]*item[V]),
			evictList: list.New(),
			evictMap:  make(map[K]*list.Element),
			freqMap:   make(map[K]int),
			keysOrder: make([]K, 0),
			config:    &config,
			cache:     c,
		}
	}

	// 启动清理过期项目的后台goroutine
	go c.cleanupLoop()

	return c
}

// getShard 根据键获取对应的分片
func (c *Cache[K, V]) getShard(key K) *shard[K, V] {
	h := fnv.New32a()
	fmt.Fprintf(h, "%v", key)
	index := h.Sum32() % uint32(c.config.ShardCount)
	return c.shards[index]
}

// Set 设置缓存值
func (c *Cache[K, V]) Set(key K, value V, ttl ...time.Duration) error {
	expiration := c.config.DefaultTTL
	if len(ttl) > 0 {
		expiration = ttl[0]
	}

	s := c.getShard(key)
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 计算大小
	size := c.config.SizeFunc(key, value)

	// 如果键已存在，先删除并更新统计信息
	if old, found := s.items[key]; found {
		s.totalSize -= old.size
		s.removeElement(key, EvictionReasonManual)
	}

	// 检查是否超过内存限制
	if c.config.MaxMemoryBytes > 0 && s.totalSize+size > c.config.MaxMemoryBytes/int64(c.config.ShardCount) {
		s.evict(EvictionReasonMemory)
		// 再次检查
		if s.totalSize+size > c.config.MaxMemoryBytes/int64(c.config.ShardCount) {
			return errors.New("无法添加项目：内存限制")
		}
	}

	// 检查是否超过数量限制
	if c.config.MaxItems > 0 && len(s.items) >= c.config.MaxItems/c.config.ShardCount {
		s.evict(EvictionReasonCapacity)
		// 再次检查
		if len(s.items) >= c.config.MaxItems/c.config.ShardCount {
			return errors.New("无法添加项目：容量限制")
		}
	}

	// 创建新的缓存项
	now := time.Now()
	it := &item[V]{
		value:      value,
		size:       size,
		expireAt:   now.Add(expiration),
		lastAccess: now,
		accessFreq: 1,
		createdAt:  now,
		accessHits: 1,
		lastHitAt:  now,
	}

	s.items[key] = it
	s.totalSize += size

	// 根据淘汰策略更新数据结构
	switch c.config.EvictionPolicy {
	case LRU, FIFO:
		element := s.evictList.PushBack(key)
		s.evictMap[key] = element
	case LFU:
		s.freqMap[key] = 1
	}

	if c.config.EvictionPolicy == FIFO || c.config.EvictionPolicy == Random {
		s.keysOrder = append(s.keysOrder, key)
	}

	// 通知其他节点（分布式同步）
	select {
	case c.syncCh <- struct{}{}:
		// 信号已发送
	default:
		// 通道已满，忽略
	}

	return nil
}

// Get 获取缓存值
func (c *Cache[K, V]) Get(ctx context.Context, key K) (V, bool) {
	// 尝试从缓存获取
	value, found, expired := c.getFromCache(key)

	// 如果找到且未过期，更新统计并返回
	if found && !expired {
		atomic.AddInt64(&c.metrics.Hits, 1)
		return value, true
	}

	// 缓存未命中，增加计数
	atomic.AddInt64(&c.metrics.Misses, 1)

	// 存在fallback函数则尝试它们
	var err error
	if len(c.fallbacks) > 0 {
		for _, fb := range c.fallbacks {
			value, err = fb(ctx, key)
			if err == nil {
				// 回源成功，写入缓存
				_ = c.Set(key, value)
				return value, true
			}
		}
	}

	// 返回零值和未找到标志
	var zero V
	return zero, false
}

// getFromCache 内部方法，从缓存获取值
func (c *Cache[K, V]) getFromCache(key K) (V, bool, bool) {
	s := c.getShard(key)
	s.mutex.RLock()
	it, found := s.items[key]
	s.mutex.RUnlock()

	if !found {
		var zero V
		return zero, false, false
	}

	// 检查是否过期
	now := time.Now()
	if !it.expireAt.IsZero() && it.expireAt.Before(now) {
		// 异步移除过期项
		go func() {
			s.mutex.Lock()
			defer s.mutex.Unlock()
			if _, exists := s.items[key]; exists {
				s.removeElement(key, EvictionReasonExpired)
			}
		}()
		var zero V
		return zero, true, true // 找到但已过期
	}

	// 更新访问统计
	s.mutex.Lock()
	defer s.mutex.Unlock()

	it.lastAccess = now
	it.accessFreq++
	it.accessHits++
	it.lastHitAt = now

	// 检查是否需要重置过期时间
	if c.config.ResetTTLOnHits > 0 && c.config.ResetTTLWindow > 0 {
		if it.accessHits >= c.config.ResetTTLOnHits &&
			now.Sub(it.lastHitAt) <= c.config.ResetTTLWindow {
			it.expireAt = now.Add(c.config.DefaultTTL)
			it.accessHits = 0
		}
	}

	// 根据淘汰策略更新数据结构
	if c.config.EvictionPolicy == LRU {
		if element, ok := s.evictMap[key]; ok {
			s.evictList.MoveToBack(element)
		}
	} else if c.config.EvictionPolicy == LFU {
		s.freqMap[key]++
	}

	return it.value, true, false
}

// Delete 删除缓存项
func (c *Cache[K, V]) Delete(key K) bool {
	s := c.getShard(key)
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if _, found := s.items[key]; !found {
		return false
	}

	s.removeElement(key, EvictionReasonManual)
	atomic.AddInt64(&c.metrics.ManualEvictions, 1)
	return true
}

// GetMulti 批量获取值
func (c *Cache[K, V]) GetMulti(ctx context.Context, keys []K) map[K]V {
	result := make(map[K]V, len(keys))
	var missingKeys []K

	// 先尝试从缓存获取所有键
	for _, key := range keys {
		value, found, expired := c.getFromCache(key)
		if found && !expired {
			result[key] = value
		} else {
			missingKeys = append(missingKeys, key)
		}
	}

	// 对于未命中的键，使用fallback
	if len(missingKeys) > 0 && len(c.fallbacks) > 0 {
		for _, key := range missingKeys {
			for _, fb := range c.fallbacks {
				value, err := fb(ctx, key)
				if err == nil {
					result[key] = value
					_ = c.Set(key, value) // 写入缓存
					break
				}
			}
		}
	}

	return result
}

// Clear 清空整个缓存
func (c *Cache[K, V]) Clear() {
	for _, s := range c.shards {
		s.mutex.Lock()
		// 遍历所有项目以便触发回调
		for k := range s.items {
			s.removeElement(k, EvictionReasonManual)
		}

		// 重置分片
		s.items = make(map[K]*item[V])
		s.evictList = list.New()
		s.evictMap = make(map[K]*list.Element)
		s.freqMap = make(map[K]int)
		s.keysOrder = make([]K, 0)
		s.totalSize = 0
		s.mutex.Unlock()
	}
}

// GetMetrics 获取缓存指标
func (c *Cache[K, V]) GetMetrics() Metrics {
	return Metrics{
		Hits:              atomic.LoadInt64(&c.metrics.Hits),
		Misses:            atomic.LoadInt64(&c.metrics.Misses),
		Evictions:         atomic.LoadInt64(&c.metrics.Evictions),
		ExpiredEvictions:  atomic.LoadInt64(&c.metrics.ExpiredEvictions),
		MemoryEvictions:   atomic.LoadInt64(&c.metrics.MemoryEvictions),
		CapacityEvictions: atomic.LoadInt64(&c.metrics.CapacityEvictions),
		ManualEvictions:   atomic.LoadInt64(&c.metrics.ManualEvictions),
	}
}

// Size 获取缓存中的项目数量
func (c *Cache[K, V]) Size() int {
	count := 0
	for _, s := range c.shards {
		s.mutex.RLock()
		count += len(s.items)
		s.mutex.RUnlock()
	}
	return count
}

// MemoryUsage 获取总内存使用量
func (c *Cache[K, V]) MemoryUsage() int64 {
	var total int64
	for _, s := range c.shards {
		s.mutex.RLock()
		total += s.totalSize
		s.mutex.RUnlock()
	}
	return total
}

// Stop 停止缓存的后台任务
func (c *Cache[K, V]) Stop() {
	close(c.stopCh)
}

// cleanupLoop 定期清理过期的缓存项
func (c *Cache[K, V]) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.removeExpired()
		case <-c.stopCh:
			return
		}
	}
}

// removeExpired 清除所有过期的缓存项
func (c *Cache[K, V]) removeExpired() {
	now := time.Now()

	for _, s := range c.shards {
		s.mutex.Lock()

		var keysToRemove []K
		for k, it := range s.items {
			if !it.expireAt.IsZero() && it.expireAt.Before(now) {
				keysToRemove = append(keysToRemove, k)
			}
		}

		for _, k := range keysToRemove {
			s.removeElement(k, EvictionReasonExpired)
		}

		s.mutex.Unlock()
	}
}

// evict 根据淘汰策略移除一个项目
func (s *shard[K, V]) evict(reason EvictionReason) {
	if len(s.items) == 0 {
		return
	}

	var keyToEvict K
	var found bool

	switch s.config.EvictionPolicy {
	case LRU:
		if s.evictList.Len() > 0 {
			element := s.evictList.Front()
			keyToEvict = element.Value.(K)
			found = true
		}
	case FIFO:
		if len(s.keysOrder) > 0 {
			keyToEvict = s.keysOrder[0]
			s.keysOrder = s.keysOrder[1:]
			found = true
		}
	case LFU:
		var minFreq int = -1
		for k, freq := range s.freqMap {
			if minFreq == -1 || freq < minFreq {
				minFreq = freq
				keyToEvict = k
				found = true
			}
		}
	case Random:
		if len(s.keysOrder) > 0 {
			index := int(time.Now().UnixNano()) % len(s.keysOrder)
			keyToEvict = s.keysOrder[index]
			// 从数组中移除此项
			s.keysOrder[index] = s.keysOrder[len(s.keysOrder)-1]
			s.keysOrder = s.keysOrder[:len(s.keysOrder)-1]
			found = true
		}
	case TTL:
		// 仅淘汰过期项，这里什么都不做
		return
	}

	if found {
		s.removeElement(keyToEvict, reason)
	}
}

// removeElement 从所有相关数据结构中移除一个缓存项
func (s *shard[K, V]) removeElement(key K, reason EvictionReason) {
	it, exists := s.items[key]
	if !exists {
		return
	}

	// 从各种数据结构中移除
	if s.config.EvictionPolicy == LRU || s.config.EvictionPolicy == FIFO {
		if element, ok := s.evictMap[key]; ok {
			s.evictList.Remove(element)
			delete(s.evictMap, key)
		}
	}

	if s.config.EvictionPolicy == LFU {
		delete(s.freqMap, key)
	}

	// 更新内存使用统计
	s.totalSize -= it.size

	// 执行淘汰回调
	if s.config.EvictionHandler != nil {
		s.config.EvictionHandler(key, it.value, reason)
	}

	// 最后从items中删除
	delete(s.items, key)

	// 更新统计信息
	atomic.AddInt64(&s.cache.metrics.Evictions, 1)

	switch reason {
	case EvictionReasonExpired:
		atomic.AddInt64(&s.cache.metrics.ExpiredEvictions, 1)
	case EvictionReasonCapacity:
		atomic.AddInt64(&s.cache.metrics.CapacityEvictions, 1)
	case EvictionReasonMemory:
		atomic.AddInt64(&s.cache.metrics.MemoryEvictions, 1)
	case EvictionReasonManual:
		atomic.AddInt64(&s.cache.metrics.ManualEvictions, 1)
	}
}

// GetWithFallback 尝试从缓存获取，不存在则使用回源函数
func (c *Cache[K, V]) GetWithFallback(ctx context.Context, key K, fb FallbackFunc[K, V]) (V, error) {
	// 先尝试从缓存获取
	value, found, expired := c.getFromCache(key)
	if found && !expired {
		atomic.AddInt64(&c.metrics.Hits, 1)
		return value, nil
	}

	atomic.AddInt64(&c.metrics.Misses, 1)

	// 使用提供的回源函数
	value, err := fb(ctx, key)
	if err != nil {
		var zero V
		return zero, err
	}

	// 写入缓存
	_ = c.Set(key, value)
	return value, nil
}

// 分布式同步相关功能
// 这里只提供一个简单的接口，实际实现需要根据具体需求定制
// SyncFrom 从另一个节点接收同步数据
func (c *Cache[K, V]) SyncFrom(data map[K]V) {
	for k, v := range data {
		_ = c.Set(k, v)
	}
}

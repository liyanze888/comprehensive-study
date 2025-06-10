package yz_v2

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// EvictionReason 淘汰原因枚举
type EvictionReason int

const (
	EvictionReasonExpired  EvictionReason = iota // TTL过期
	EvictionReasonCapacity                       // 容量限制
	EvictionReasonMemory                         // 内存限制
	EvictionReasonManual                         // 手动移除
)

func (r EvictionReason) String() string {
	switch r {
	case EvictionReasonExpired:
		return "Expired"
	case EvictionReasonCapacity:
		return "Capacity"
	case EvictionReasonMemory:
		return "Memory"
	case EvictionReasonManual:
		return "Manual"
	default:
		return "Unknown"
	}
}

// EvictPolicy 淘汰策略
type EvictPolicy int

const (
	LRU    EvictPolicy = iota // 最近最少使用
	LFU                       // 最不常用
	FIFO                      // 先进先出
	Random                    // 随机淘汰
	TTL                       // 仅基于过期时间
	ARC                       // Adaptive Replacement Cache
	TwoQ                      // 2Q算法
)

// Hashable 可哈希接口，支持范型key
type Hashable interface {
	Hash() string
	Equal(other interface{}) bool
	String() string
	comparable
}

// StringKey 字符串键的实现
type StringKey string

func (k StringKey) Hash() string {
	return string(k)
}

func (k StringKey) Equal(other interface{}) bool {
	if o, ok := other.(StringKey); ok {
		return k == o
	}
	return false
}

func (k StringKey) String() string {
	return string(k)
}

// IntKey 整数键的实现
type IntKey int64

func (k IntKey) Hash() string {
	return fmt.Sprintf("%d", k)
}

func (k IntKey) Equal(other interface{}) bool {
	if o, ok := other.(IntKey); ok {
		return k == o
	}
	return false
}

func (k IntKey) String() string {
	return fmt.Sprintf("%d", k)
}

// AccessRecord 访问记录
type AccessRecord struct {
	mu          sync.RWMutex
	timestamps  []int64
	lastCleanup time.Time
}

// AddAccess 添加访问记录（线程安全）
func (ar *AccessRecord) AddAccess(now time.Time, window time.Duration) int {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	nowNano := now.UnixNano()
	windowNano := int64(window)

	// 清理过期记录（每秒最多清理一次）
	if now.Sub(ar.lastCleanup) > time.Second {
		validTimestamps := ar.timestamps[:0] // 重用slice
		cutoff := nowNano - windowNano
		for _, timestamp := range ar.timestamps {
			if timestamp >= cutoff {
				validTimestamps = append(validTimestamps, timestamp)
			}
		}
		ar.timestamps = validTimestamps
		ar.lastCleanup = now
	}

	// 添加当前访问
	ar.timestamps = append(ar.timestamps, nowNano)

	return len(ar.timestamps)
}

// Clear 清理访问记录
func (ar *AccessRecord) Clear() {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.timestamps = ar.timestamps[:0]
}

// CacheItem 缓存项
type CacheItem[K Hashable, V any] struct {
	Key         K
	Value       V
	ExpireTime  time.Time
	AccessTime  time.Time
	CreateTime  time.Time
	AccessCount int64
	Size        int64
	mu          sync.RWMutex
	prev        *CacheItem[K, V]
	next        *CacheItem[K, V]
}

// IsExpired 检查是否过期
func (item *CacheItem[K, V]) IsExpired() bool {
	item.mu.RLock()
	defer item.mu.RUnlock()
	if item.ExpireTime.IsZero() {
		return false
	}
	return time.Now().After(item.ExpireTime)
}

// Touch 更新访问时间和计数
func (item *CacheItem[K, V]) Touch() {
	item.mu.Lock()
	defer item.mu.Unlock()
	item.AccessTime = time.Now()
	atomic.AddInt64(&item.AccessCount, 1)
}

// UpdateExpireTime 更新过期时间（线程安全）
func (item *CacheItem[K, V]) UpdateExpireTime(newExpireTime time.Time) {
	item.mu.Lock()
	defer item.mu.Unlock()
	item.ExpireTime = newExpireTime
}

// GetSize 获取项目大小
func (item *CacheItem[K, V]) GetSize() int64 {
	return item.Size + int64(len(item.Key.Hash())) + int64(unsafe.Sizeof(*item))
}

// EvictionCallback 淘汰回调函数
type EvictionCallback[K Hashable, V any] func(key K, value V, reason EvictionReason)

// FallbackFunc 回退函数
type FallbackFunc[K Hashable, V any] func(ctx context.Context, key K) (V, error)

// BatchFallbackFunc 批量回退函数
type BatchFallbackFunc[K Hashable, V any] func(ctx context.Context, keys []K) (map[string]V, error)

// RemoteCache 远程缓存接口
type RemoteCache[K Hashable, V any] interface {
	Get(ctx context.Context, key K) (V, bool, error)
	Set(ctx context.Context, key K, value V, ttl time.Duration) error
	Delete(ctx context.Context, key K) error
	BatchGet(ctx context.Context, keys []K) (map[string]V, error)
	BatchSet(ctx context.Context, items map[K]V, ttl time.Duration) error
	BatchDelete(ctx context.Context, keys []K) error
}

// DistributedSync 分布式同步接口
type DistributedSync[K Hashable, V any] interface {
	NotifySet(key K, value V, ttl time.Duration) error
	NotifyDelete(key K) error
	Subscribe(callback func(key K, value V, operation string)) error
	Close() error
}

// CacheConfig 缓存配置
type CacheConfig[K Hashable, V any] struct {
	MaxSize         int                    // 最大数据项数量
	MaxMemory       int64                  // 最大内存大小
	DefaultTTL      time.Duration          // 默认TTL
	CleanupInterval time.Duration          // 清理间隔
	ShardCount      int                    // 分片数量
	EvictPolicy     EvictPolicy            // 淘汰策略
	OnEvicted       EvictionCallback[K, V] // 淘汰回调
	RemoteCache     RemoteCache[K, V]      // 远程缓存
	DistributedSync DistributedSync[K, V]  // 分布式同步
	EnableMetrics   bool                   // 启用指标收集
	AccessThreshold int                    // N秒内访问M次的M值
	AccessWindow    time.Duration          // N秒内访问M次的N值
}

// Metrics 指标统计
type Metrics struct {
	Hits          int64 // 命中次数
	Misses        int64 // 未命中次数
	Sets          int64 // 设置次数
	Deletes       int64 // 删除次数
	Evictions     int64 // 淘汰次数
	MemoryUsage   int64 // 内存使用量
	ItemCount     int64 // 数据项数量
	RemoteCalls   int64 // 远程调用次数
	FallbackCalls int64 // 回退调用次数
	mu            sync.RWMutex
}

// GetHitRate 获取命中率
func (m *Metrics) GetHitRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := m.Hits + m.Misses
	if total == 0 {
		return 0
	}
	return float64(m.Hits) / float64(total)
}

// CacheShard 缓存分片
type CacheShard[K Hashable, V any] struct {
	items       map[string]*CacheItem[K, V]
	lruHead     *CacheItem[K, V]
	lruTail     *CacheItem[K, V]
	lfuFreqs    map[int64]*CacheItem[K, V] // LFU频率链表
	fifoHead    *CacheItem[K, V]           // FIFO队列头
	fifoTail    *CacheItem[K, V]           // FIFO队列尾
	mu          sync.RWMutex
	memoryUsage int64
}

// newCacheShard 创建缓存分片
func newCacheShard[K Hashable, V any]() *CacheShard[K, V] {
	shard := &CacheShard[K, V]{
		items:    make(map[string]*CacheItem[K, V]),
		lfuFreqs: make(map[int64]*CacheItem[K, V]),
	}
	// 初始化LRU双向链表
	shard.lruHead = &CacheItem[K, V]{}
	shard.lruTail = &CacheItem[K, V]{}
	shard.lruHead.next = shard.lruTail
	shard.lruTail.prev = shard.lruHead
	return shard
}

// MultiLevelCache 多级缓存
type MultiLevelCache[K Hashable, V any] struct {
	shards          []*CacheShard[K, V]
	config          *CacheConfig[K, V]
	metrics         *Metrics
	cleanupTicker   *time.Ticker
	cleanupStop     chan struct{}
	fallbackFuncs   []FallbackFunc[K, V]
	batchFallbackFn BatchFallbackFunc[K, V]
	accessTracker   sync.Map // key: string -> *AccessRecord
	wg              sync.WaitGroup
}

// NewMultiLevelCache 创建多级缓存
func NewMultiLevelCache[K Hashable, V any](config *CacheConfig[K, V]) *MultiLevelCache[K, V] {
	if config.ShardCount <= 0 {
		config.ShardCount = 16
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = time.Minute
	}

	cache := &MultiLevelCache[K, V]{
		config:        config,
		metrics:       &Metrics{},
		cleanupStop:   make(chan struct{}),
		fallbackFuncs: make([]FallbackFunc[K, V], 0),
	}

	// 初始化分片
	cache.shards = make([]*CacheShard[K, V], config.ShardCount)
	for i := 0; i < config.ShardCount; i++ {
		cache.shards[i] = newCacheShard[K, V]()
	}

	// 启动清理协程
	cache.startCleanup()

	return cache
}

// getShard 获取分片
func (c *MultiLevelCache[K, V]) getShard(key K) *CacheShard[K, V] {
	hash := fnv.New32a()
	hash.Write([]byte(key.Hash()))
	return c.shards[hash.Sum32()%uint32(c.config.ShardCount)]
}

// getKeyHash 获取key的哈希值
func (c *MultiLevelCache[K, V]) getKeyHash(key K) string {
	return key.Hash()
}

// Set 设置缓存
func (c *MultiLevelCache[K, V]) Set(key K, value V, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = c.config.DefaultTTL
	}

	shard := c.getShard(key)
	keyHash := c.getKeyHash(key)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()
	expireTime := time.Time{}
	if ttl > 0 {
		expireTime = now.Add(ttl)
	}

	// 计算大小
	size := c.calculateSize(value)

	// 创建新项
	item := &CacheItem[K, V]{
		Key:        key,
		Value:      value,
		ExpireTime: expireTime,
		AccessTime: now,
		CreateTime: now,
		Size:       size,
	}

	// 检查是否已存在
	if oldItem, exists := shard.items[keyHash]; exists {
		// 更新现有项
		atomic.AddInt64(&shard.memoryUsage, size-oldItem.Size)
		c.removeFromPolicy(shard, oldItem)
		shard.items[keyHash] = item
	} else {
		// 添加新项
		atomic.AddInt64(&shard.memoryUsage, size)
		shard.items[keyHash] = item
		atomic.AddInt64(&c.metrics.ItemCount, 1)
	}

	// 添加到淘汰策略中
	c.addToPolicy(shard, item)

	// 检查容量和内存限制
	c.evictIfNeeded(shard)

	// 更新指标
	atomic.AddInt64(&c.metrics.Sets, 1)
	atomic.StoreInt64(&c.metrics.MemoryUsage, c.getTotalMemoryUsage())

	// 远程缓存同步
	if c.config.RemoteCache != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.config.RemoteCache.Set(ctx, key, value, ttl)
		}()
	}

	// 分布式同步
	if c.config.DistributedSync != nil {
		go func() {
			c.config.DistributedSync.NotifySet(key, value, ttl)
		}()
	}

	return nil
}

// Get 获取缓存
func (c *MultiLevelCache[K, V]) Get(key K) (V, bool) {
	return c.GetWithFallback(context.Background(), key)
}

// GetWithFallback 带回退的获取
func (c *MultiLevelCache[K, V]) GetWithFallback(ctx context.Context, key K) (V, bool) {
	var zero V

	// 1. 尝试从本地缓存获取
	if value, found := c.getLocal(key); found {
		atomic.AddInt64(&c.metrics.Hits, 1)
		return value, true
	}

	atomic.AddInt64(&c.metrics.Misses, 1)

	// 2. 尝试从远程缓存获取
	if c.config.RemoteCache != nil {
		atomic.AddInt64(&c.metrics.RemoteCalls, 1)
		if value, found, err := c.config.RemoteCache.Get(ctx, key); err == nil && found {
			// 将数据写入本地缓存
			c.Set(key, value, c.config.DefaultTTL)
			return value, true
		}
	}

	// 3. 尝试回退函数链
	for _, fallbackFn := range c.fallbackFuncs {
		atomic.AddInt64(&c.metrics.FallbackCalls, 1)
		if value, err := fallbackFn(ctx, key); err == nil {
			// 写入所有级别缓存
			c.writeToAllLevels(ctx, key, value, c.config.DefaultTTL)
			return value, true
		}
	}

	return zero, false
}

// getLocal 从本地获取
func (c *MultiLevelCache[K, V]) getLocal(key K) (V, bool) {
	var zero V
	shard := c.getShard(key)
	keyHash := c.getKeyHash(key)

	shard.mu.RLock()
	item, exists := shard.items[keyHash]
	if !exists {
		shard.mu.RUnlock()
		return zero, false
	}

	// 检查过期
	if item.IsExpired() {
		shard.mu.RUnlock()
		c.Delete(key)
		return zero, false
	}

	value := item.Value
	shard.mu.RUnlock()

	// 更新访问信息
	item.Touch()
	c.updateAccessFrequency(key, item)
	c.updatePolicy(shard, item)

	return value, true
}

// updateAccessFrequency 更新访问频率（修复并发问题）
func (c *MultiLevelCache[K, V]) updateAccessFrequency(key K, item *CacheItem[K, V]) {
	if c.config.AccessThreshold <= 0 || c.config.AccessWindow <= 0 {
		return
	}

	keyHash := c.getKeyHash(key)
	now := time.Now()

	// 获取或创建访问记录
	accessRecordInterface, _ := c.accessTracker.LoadOrStore(keyHash, &AccessRecord{})
	accessRecord := accessRecordInterface.(*AccessRecord)

	// 添加访问记录并获取当前访问次数
	accessCount := accessRecord.AddAccess(now, c.config.AccessWindow)

	// 检查是否达到阈值
	if accessCount >= c.config.AccessThreshold {
		// 重置TTL - 这里已经是线程安全的
		if c.config.DefaultTTL > 0 {
			newExpireTime := now.Add(c.config.DefaultTTL)
			item.UpdateExpireTime(newExpireTime)
		}

		// 清理访问记录
		accessRecord.Clear()
		c.accessTracker.Delete(keyHash)
	}
}

// BatchGet 批量获取
func (c *MultiLevelCache[K, V]) BatchGet(ctx context.Context, keys []K) map[string]V {
	result := make(map[string]V)
	missingKeys := make([]K, 0)

	// 1. 从本地缓存批量获取
	for _, key := range keys {
		if value, found := c.getLocal(key); found {
			result[c.getKeyHash(key)] = value
			atomic.AddInt64(&c.metrics.Hits, 1)
		} else {
			missingKeys = append(missingKeys, key)
			atomic.AddInt64(&c.metrics.Misses, 1)
		}
	}

	if len(missingKeys) == 0 {
		return result
	}

	// 2. 从远程缓存批量获取
	if c.config.RemoteCache != nil {
		atomic.AddInt64(&c.metrics.RemoteCalls, 1)
		if remoteResult, err := c.config.RemoteCache.BatchGet(ctx, missingKeys); err == nil {
			for keyHash, value := range remoteResult {
				result[keyHash] = value
			}
			// 将远程数据写入本地缓存
			for _, key := range missingKeys {
				keyHash := c.getKeyHash(key)
				if value, found := remoteResult[keyHash]; found {
					go c.Set(key, value, c.config.DefaultTTL)
				}
			}
			// 更新缺失键列表
			newMissingKeys := make([]K, 0)
			for _, key := range missingKeys {
				keyHash := c.getKeyHash(key)
				if _, found := remoteResult[keyHash]; !found {
					newMissingKeys = append(newMissingKeys, key)
				}
			}
			missingKeys = newMissingKeys
		}
	}

	// 3. 使用批量回退函数
	if len(missingKeys) > 0 && c.batchFallbackFn != nil {
		atomic.AddInt64(&c.metrics.FallbackCalls, 1)
		if fallbackResult, err := c.batchFallbackFn(ctx, missingKeys); err == nil {
			for keyHash, value := range fallbackResult {
				result[keyHash] = value
			}
			// 将回退数据写入所有级别缓存
			for _, key := range missingKeys {
				keyHash := c.getKeyHash(key)
				if value, found := fallbackResult[keyHash]; found {
					go c.writeToAllLevels(ctx, key, value, c.config.DefaultTTL)
				}
			}
		}
	}

	return result
}

// writeToAllLevels 写入所有级别缓存
func (c *MultiLevelCache[K, V]) writeToAllLevels(ctx context.Context, key K, value V, ttl time.Duration) {
	// 写入本地缓存
	c.Set(key, value, ttl)

	// 写入远程缓存
	if c.config.RemoteCache != nil {
		go func() {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			c.config.RemoteCache.Set(ctx, key, value, ttl)
		}()
	}
}

// Delete 删除缓存
func (c *MultiLevelCache[K, V]) Delete(key K) bool {
	shard := c.getShard(key)
	keyHash := c.getKeyHash(key)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	item, exists := shard.items[keyHash]
	if !exists {
		return false
	}

	// 从分片中删除
	delete(shard.items, keyHash)
	atomic.AddInt64(&shard.memoryUsage, -item.GetSize())
	atomic.AddInt64(&c.metrics.ItemCount, -1)

	// 从淘汰策略中删除
	c.removeFromPolicy(shard, item)

	// 清理访问记录
	c.accessTracker.Delete(keyHash)

	// 触发回调
	if c.config.OnEvicted != nil {
		go c.config.OnEvicted(key, item.Value, EvictionReasonManual)
	}

	// 更新指标
	atomic.AddInt64(&c.metrics.Deletes, 1)
	atomic.StoreInt64(&c.metrics.MemoryUsage, c.getTotalMemoryUsage())

	// 远程缓存同步
	if c.config.RemoteCache != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.config.RemoteCache.Delete(ctx, key)
		}()
	}

	// 分布式同步
	if c.config.DistributedSync != nil {
		go func() {
			c.config.DistributedSync.NotifyDelete(key)
		}()
	}

	return true
}

// calculateSize 计算值的大小
func (c *MultiLevelCache[K, V]) calculateSize(value V) int64 {
	// 简化的大小计算，实际应用中可能需要更精确的计算
	data, _ := json.Marshal(value)
	return int64(len(data))
}

// 其他方法保持不变，但需要更新相关的泛型签名...
// [省略其他方法的实现，因为它们主要是签名更新]

// addToPolicy 添加到淘汰策略
func (c *MultiLevelCache[K, V]) addToPolicy(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	switch c.config.EvictPolicy {
	case LRU:
		c.addToLRU(shard, item)
	case FIFO:
		c.addToFIFO(shard, item)
	case LFU:
		c.addToLFU(shard, item)
	}
}

// removeFromPolicy 从淘汰策略中删除
func (c *MultiLevelCache[K, V]) removeFromPolicy(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	switch c.config.EvictPolicy {
	case LRU:
		c.removeFromLRU(item)
	case FIFO:
		c.removeFromFIFO(shard, item)
	case LFU:
		c.removeFromLFU(shard, item)
	}
}

// updatePolicy 更新淘汰策略
func (c *MultiLevelCache[K, V]) updatePolicy(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	switch c.config.EvictPolicy {
	case LRU:
		shard.mu.Lock()
		c.moveToLRUHead(shard, item)
		shard.mu.Unlock()
	case LFU:
		shard.mu.Lock()
		c.updateLFU(shard, item)
		shard.mu.Unlock()
	}
}

// LRU 相关方法
func (c *MultiLevelCache[K, V]) addToLRU(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	item.next = shard.lruHead.next
	item.prev = shard.lruHead
	shard.lruHead.next.prev = item
	shard.lruHead.next = item
}

func (c *MultiLevelCache[K, V]) removeFromLRU(item *CacheItem[K, V]) {
	if item.prev != nil && item.next != nil {
		item.prev.next = item.next
		item.next.prev = item.prev
		item.prev = nil
		item.next = nil
	}
}

func (c *MultiLevelCache[K, V]) moveToLRUHead(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	c.removeFromLRU(item)
	c.addToLRU(shard, item)
}

// FIFO 相关方法
func (c *MultiLevelCache[K, V]) addToFIFO(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	if shard.fifoTail == nil {
		shard.fifoHead = item
		shard.fifoTail = item
	} else {
		shard.fifoTail.next = item
		item.prev = shard.fifoTail
		shard.fifoTail = item
	}
}

func (c *MultiLevelCache[K, V]) removeFromFIFO(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	if item.prev != nil {
		item.prev.next = item.next
	} else {
		shard.fifoHead = item.next
	}
	if item.next != nil {
		item.next.prev = item.prev
	} else {
		shard.fifoTail = item.prev
	}
	item.prev = nil
	item.next = nil
}

// LFU 相关方法
func (c *MultiLevelCache[K, V]) addToLFU(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	freq := atomic.LoadInt64(&item.AccessCount)
	if freq == 0 {
		freq = 1
		atomic.StoreInt64(&item.AccessCount, freq)
	}
	shard.lfuFreqs[freq] = item
}

func (c *MultiLevelCache[K, V]) removeFromLFU(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	freq := atomic.LoadInt64(&item.AccessCount)
	delete(shard.lfuFreqs, freq)
}

func (c *MultiLevelCache[K, V]) updateLFU(shard *CacheShard[K, V], item *CacheItem[K, V]) {
	oldFreq := atomic.LoadInt64(&item.AccessCount) - 1
	newFreq := atomic.LoadInt64(&item.AccessCount)
	delete(shard.lfuFreqs, oldFreq)
	shard.lfuFreqs[newFreq] = item
}

// evictIfNeeded 根据需要淘汰数据
func (c *MultiLevelCache[K, V]) evictIfNeeded(shard *CacheShard[K, V]) {
	// 检查数量限制
	if c.config.MaxSize > 0 {
		itemCount := atomic.LoadInt64(&c.metrics.ItemCount)
		if itemCount > int64(c.config.MaxSize) {
			c.evictByPolicy(shard, EvictionReasonCapacity)
		}
	}

	// 检查内存限制
	if c.config.MaxMemory > 0 {
		memoryUsage := atomic.LoadInt64(&shard.memoryUsage)
		if memoryUsage > c.config.MaxMemory/int64(c.config.ShardCount) {
			c.evictByPolicy(shard, EvictionReasonMemory)
		}
	}
}

// evictByPolicy 根据策略淘汰
func (c *MultiLevelCache[K, V]) evictByPolicy(shard *CacheShard[K, V], reason EvictionReason) {
	var victimKey string
	var victim *CacheItem[K, V]

	switch c.config.EvictPolicy {
	case LRU:
		victim = shard.lruTail.prev
		if victim != shard.lruHead {
			victimKey = victim.Key.Hash()
		}
	case FIFO:
		victim = shard.fifoHead
		if victim != nil {
			victimKey = victim.Key.Hash()
		}
	case LFU:
		minFreq := int64(-1)
		for freq, item := range shard.lfuFreqs {
			if minFreq == -1 || freq < minFreq {
				minFreq = freq
				victim = item
				victimKey = item.Key.Hash()
			}
		}
	case Random:
		// 随机选择一个key
		keys := make([]string, 0, len(shard.items))
		for key := range shard.items {
			keys = append(keys, key)
		}
		if len(keys) > 0 {
			victimKey = keys[rand.Intn(len(keys))]
			victim = shard.items[victimKey]
		}
	case TTL:
		// 选择最早过期的项
		var earliestExpire time.Time
		for key, item := range shard.items {
			if !item.ExpireTime.IsZero() && (earliestExpire.IsZero() || item.ExpireTime.Before(earliestExpire)) {
				earliestExpire = item.ExpireTime
				victimKey = key
				victim = item
			}
		}
	}

	if victimKey != "" && victim != nil {
		// 删除选中的项
		delete(shard.items, victimKey)
		atomic.AddInt64(&shard.memoryUsage, -victim.GetSize())
		atomic.AddInt64(&c.metrics.ItemCount, -1)
		atomic.AddInt64(&c.metrics.Evictions, 1)

		// 从淘汰策略中删除
		c.removeFromPolicy(shard, victim)

		// 清理访问记录
		c.accessTracker.Delete(victimKey)

		// 触发回调
		if c.config.OnEvicted != nil {
			go c.config.OnEvicted(victim.Key, victim.Value, reason)
		}
	}
}

// getTotalMemoryUsage 获取总内存使用量
func (c *MultiLevelCache[K, V]) getTotalMemoryUsage() int64 {
	total := int64(0)
	for _, shard := range c.shards {
		total += atomic.LoadInt64(&shard.memoryUsage)
	}
	return total
}

// startCleanup 启动清理协程
func (c *MultiLevelCache[K, V]) startCleanup() {
	c.cleanupTicker = time.NewTicker(c.config.CleanupInterval)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.cleanupTicker.C:
				c.cleanup()
			case <-c.cleanupStop:
				return
			}
		}
	}()
}

// cleanup 清理过期数据
func (c *MultiLevelCache[K, V]) cleanup() {
	for _, shard := range c.shards {
		shard.mu.Lock()
		expiredKeys := make([]string, 0)
		for keyHash, item := range shard.items {
			if item.IsExpired() {
				expiredKeys = append(expiredKeys, keyHash)
			}
		}

		for _, keyHash := range expiredKeys {
			if item, exists := shard.items[keyHash]; exists {
				delete(shard.items, keyHash)
				atomic.AddInt64(&shard.memoryUsage, -item.GetSize())
				atomic.AddInt64(&c.metrics.ItemCount, -1)
				atomic.AddInt64(&c.metrics.Evictions, 1)

				c.removeFromPolicy(shard, item)

				// 清理访问记录
				c.accessTracker.Delete(keyHash)

				// 触发回调
				if c.config.OnEvicted != nil {
					go c.config.OnEvicted(item.Key, item.Value, EvictionReasonExpired)
				}
			}
		}
		shard.mu.Unlock()
	}
	atomic.StoreInt64(&c.metrics.MemoryUsage, c.getTotalMemoryUsage())
}

// AddFallback 添加回退函数
func (c *MultiLevelCache[K, V]) AddFallback(fn FallbackFunc[K, V]) {
	c.fallbackFuncs = append(c.fallbackFuncs, fn)
}

// SetBatchFallback 设置批量回退函数
func (c *MultiLevelCache[K, V]) SetBatchFallback(fn BatchFallbackFunc[K, V]) {
	c.batchFallbackFn = fn
}

// GetMetrics 获取指标
func (c *MultiLevelCache[K, V]) GetMetrics() *Metrics {
	return c.metrics
}

// Clear 清空缓存
func (c *MultiLevelCache[K, V]) Clear() {
	for _, shard := range c.shards {
		shard.mu.Lock()
		for keyHash, item := range shard.items {
			if c.config.OnEvicted != nil {
				go c.config.OnEvicted(item.Key, item.Value, EvictionReasonManual)
			}
			// 清理访问记录
			c.accessTracker.Delete(keyHash)
		}
		shard.items = make(map[string]*CacheItem[K, V])
		shard.memoryUsage = 0
		shard.mu.Unlock()
	}
	atomic.StoreInt64(&c.metrics.ItemCount, 0)
	atomic.StoreInt64(&c.metrics.MemoryUsage, 0)
}

// Close 关闭缓存
func (c *MultiLevelCache[K, V]) Close() error {
	// 停止清理协程
	close(c.cleanupStop)
	if c.cleanupTicker != nil {
		c.cleanupTicker.Stop()
	}
	c.wg.Wait()

	// 关闭分布式同步
	if c.config.DistributedSync != nil {
		return c.config.DistributedSync.Close()
	}

	return nil
}

// Keys 获取所有键
func (c *MultiLevelCache[K, V]) Keys() []K {
	keys := make([]K, 0)
	for _, shard := range c.shards {
		shard.mu.RLock()
		for _, item := range shard.items {
			keys = append(keys, item.Key)
		}
		shard.mu.RUnlock()
	}
	return keys
}

// Size 获取缓存大小
func (c *MultiLevelCache[K, V]) Size() int {
	return int(atomic.LoadInt64(&c.metrics.ItemCount))
}

// MemoryUsage 获取内存使用量
func (c *MultiLevelCache[K, V]) MemoryUsage() int64 {
	return atomic.LoadInt64(&c.metrics.MemoryUsage)
}

// 使用示例
func ExampleUsage() {
	// 使用字符串key的缓存
	config := &CacheConfig[StringKey, string]{
		MaxSize:         1000,
		MaxMemory:       100 * 1024 * 1024, // 100MB
		DefaultTTL:      time.Hour,
		CleanupInterval: time.Minute * 5,
		ShardCount:      16,
		EvictPolicy:     LRU,
		EnableMetrics:   true,
		AccessThreshold: 5,           // 5次访问
		AccessWindow:    time.Minute, // 1分钟内
		OnEvicted: func(key StringKey, value string, reason EvictionReason) {
			fmt.Printf("Key %s evicted, reason: %s\n", key.String(), reason.String())
		},
	}

	// 创建缓存实例
	cache := NewMultiLevelCache[StringKey, string](config)
	defer cache.Close()

	// 添加回退函数
	cache.AddFallback(func(ctx context.Context, key StringKey) (string, error) {
		// 模拟从数据库获取数据
		return fmt.Sprintf("value_from_db_%s", key.String()), nil
	})

	// 使用缓存
	ctx := context.Background()

	// 设置缓存
	cache.Set(StringKey("key1"), "value1", time.Minute)

	// 获取缓存
	if value, found := cache.Get(StringKey("key1")); found {
		fmt.Printf("Found in cache: %s\n", value)
	}

	// 获取不存在的缓存（会触发回退函数）
	if value, found := cache.GetWithFallback(ctx, StringKey("key2")); found {
		fmt.Printf("Found via fallback: %s\n", value)
	}

	// 使用整数key的缓存
	intConfig := &CacheConfig[IntKey, int]{
		MaxSize:         500,
		DefaultTTL:      time.Hour,
		EvictPolicy:     LRU,
		AccessThreshold: 3,
		AccessWindow:    time.Minute,
	}

	intCache := NewMultiLevelCache[IntKey, int](intConfig)
	defer intCache.Close()

	// 设置整数缓存
	intCache.Set(IntKey(123), 456, time.Minute)

	// 获取整数缓存
	if value, found := intCache.Get(IntKey(123)); found {
		fmt.Printf("Found int value: %d\n", value)
	}

	// 获取指标
	metrics := cache.GetMetrics()
	fmt.Printf("Hit rate: %.2f%%\n", metrics.GetHitRate()*100)
	fmt.Printf("Item count: %d\n", cache.Size())
	fmt.Printf("Memory usage: %d bytes\n", cache.MemoryUsage())
}

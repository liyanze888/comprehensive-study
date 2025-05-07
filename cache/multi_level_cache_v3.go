package cache

import (
	"container/heap"
	"container/list"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sync"
	"time"
)

/**
1. 淘汰策略支持  (1)LRU - 最近最少使用淘汰策略(2) LFU - 最不常用淘汰策略 (3)FIFO - 先进先出淘汰策略(4)Random - 随机淘汰策略(5)TTL - 仅基于过期时间淘汰2. 支持设置淘汰时间,超时自动淘汰。3. 支持在N秒内,访问M次,重置超时时间。4. 支持设置内存上限5. 支持设置数据项数量。6. 支持淘汰数据通知。 需要知道淘汰原因(1) EvictionReasonExpired - 项目因TTL过期而被淘汰 (2) EvictionReasonCapacity - 项目因容量限制而被淘汰 (3)EvictionReasonMemory - 项目因内存限制而被淘汰 (4)EvictionReasonManual - 项目被手动移除7. 支持分布式同步8.内存中储存的map 使用分片进行保存,减少锁的粒度,同时锁也是分段锁9. key 和value支持范型10.支持批量获取11.支持多集缓存12.支持fallback,如果cache中不存在则请求fallback函数13. fallback 支持多集缓存,如果local cache 不存在,先fallback到远程cache,如果远程cache也不存在,则返回fallback函数14. 支持多点fallback,功能同12,和1315. 远程同步和分布式同步不是同一个功能,远程同步类似于一级缓存,比如redis。分布式同步是多个实例节点需要数据一致。 如果local cache 不存在我使用获取一级缓存比如redis,如果redis中也不存在则使用fallback获取后,同步到redis 和 localcache 并且同步到所有的节点16. 实现更多的淘汰策略17. 添加监控和指标收集18.本地缓存优先,减少远程调用19. 支持批量操作减少网络开销以上19需要完整的实现并且保持代码完整,高性能,使用golang 实现,使用中文回答
*/
// 淘汰原因枚举
type EvictionReason int

const (
	EvictionReasonExpired  EvictionReason = iota // 因TTL过期而被淘汰
	EvictionReasonCapacity                       // 因容量限制而被淘汰
	EvictionReasonMemory                         // 因内存限制而被淘汰
	EvictionReasonManual                         // 手动移除
)

// 淘汰策略类型
type EvictionPolicy int

const (
	LRU    EvictionPolicy = iota // 最近最少使用
	LFU                          // 最不常用
	FIFO                         // 先进先出
	Random                       // 随机淘汰
	TTL                          // 仅基于过期时间
)

// 缓存监听器接口 - 用于淘汰通知
type CacheListener[K comparable, V any] interface {
	OnEviction(key K, value V, reason EvictionReason)
}

// 缓存项结构
type Item[V any] struct {
	value        V             // 值
	expireAt     time.Time     // 过期时间
	lastAccessed time.Time     // 最后访问时间
	accessCount  int           // 访问次数
	size         int           // 内存占用大小(字节)
	resetInfo    *ResetTTLInfo // 访问次数重置信息
}

// TTL重置信息
type ResetTTLInfo struct {
	seconds   int       // N秒内
	times     int       // 访问M次
	startTime time.Time // 开始时间
	count     int       // 当前计数
}

// 缓存分片 - 减少锁竞争
type CacheShard[K comparable, V any] struct {
	items         map[K]*Item[V]
	lruList       *list.List
	lruItems      map[K]*list.Element
	lfuHeap       *LFUHeap[K]
	fifoQueue     *list.List
	fifoItems     map[K]*list.Element
	ttlHeap       *TTLHeap[K]
	accessCounter *AccessCounter[K]
	bloomFilter   *BloomFilter
	deletedKeys   map[K]struct{}
	mutex         sync.RWMutex
	policy        EvictionPolicy
	listeners     []CacheListener[K, V]
	capacity      int
	memoryLimit   int64
	currentSize   int64
}

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

// 访问计数器
type AccessCounter[K comparable] struct {
	counts    map[K]int
	mu        sync.RWMutex
	decayRate float64 // 衰减率
}

func NewAccessCounter[K comparable](decayRate float64) *AccessCounter[K] {
	return &AccessCounter[K]{
		counts:    make(map[K]int),
		decayRate: decayRate,
	}
}

func (ac *AccessCounter[K]) Increment(key K) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.counts[key]++
}

func (ac *AccessCounter[K]) Get(key K) int {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.counts[key]
}

func (ac *AccessCounter[K]) Remove(key K) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	delete(ac.counts, key)
}

func (ac *AccessCounter[K]) Decay() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	for k, v := range ac.counts {
		ac.counts[k] = int(float64(v) * ac.decayRate)
	}
}

// 布隆过滤器
type BloomFilter struct {
	bits     []bool
	size     int
	hashFunc []func(string) uint32
}

func NewBloomFilter(size int, hashCount int) *BloomFilter {
	bf := &BloomFilter{
		bits:     make([]bool, size),
		size:     size,
		hashFunc: make([]func(string) uint32, hashCount),
	}

	// 初始化哈希函数
	for i := 0; i < hashCount; i++ {
		seed := uint32(i)
		bf.hashFunc[i] = func(s string) uint32 {
			h := fnv.New32a()
			h.Write([]byte(s))
			return h.Sum32() + seed
		}
	}

	return bf
}

func (bf *BloomFilter) Add(key string) {
	for _, hash := range bf.hashFunc {
		bf.bits[hash(key)%uint32(bf.size)] = true
	}
}

func (bf *BloomFilter) Contains(key string) bool {
	for _, hash := range bf.hashFunc {
		if !bf.bits[hash(key)%uint32(bf.size)] {
			return false
		}
	}
	return true
}

// 回调函数类型定义
type FallbackFunc[K comparable, V any] func(key K) (V, error)

// 缓存配置
type CacheConfig struct {
	ShardCount      int            // 分片数量
	EvictionPolicy  EvictionPolicy // 淘汰策略
	DefaultTTL      time.Duration  // 默认TTL
	MaxMemory       int64          // 内存上限(字节)
	MaxItems        int            // 最大项数
	CleanupInterval time.Duration  // 清理间隔
}

// 缓存结构体
type Cache[K comparable, V any] struct {
	shards       []*CacheShard[K, V]     // 分片数组
	shardCount   int                     // 分片数量
	hashFunc     func(K) uint32          // 哈希函数
	config       CacheConfig             // 配置
	remoteCaches []RemoteCache[K, V]     // 远程缓存列表
	peers        []DistributedPeer[K, V] // 分布式节点列表
	fallbacks    []FallbackFunc[K, V]    // 回调函数列表
	listeners    []CacheListener[K, V]   // 监听器列表
	metrics      *CacheMetrics           // 缓存指标
}

// 远程缓存接口
type RemoteCache[K comparable, V any] interface {
	Get(key K) (V, bool)
	Set(key K, value V, ttl time.Duration)
	Delete(key K)
	GetMulti(keys []K) map[K]V
	SetMulti(items map[K]V, ttl time.Duration)
}

// 分布式同步接口
type DistributedPeer[K comparable, V any] interface {
	SyncSet(key K, value V, ttl time.Duration)
	SyncDelete(key K)
	SyncSetMulti(items map[K]V, ttl time.Duration)
}

// 缓存指标
type CacheMetrics struct {
	hits          int64 // 命中次数
	misses        int64 // 未命中次数
	evictions     int64 // 淘汰次数
	memoryUsage   int64 // 内存使用量
	remoteHits    int64 // 远程缓存命中
	remoteMisses  int64 // 远程缓存未命中
	fallbackCalls int64 // 回调函数调用次数
	mu            sync.Mutex
}

// 创建新的缓存
func NewCache[K comparable, V any](config CacheConfig) *Cache[K, V] {
	if config.ShardCount <= 0 {
		config.ShardCount = 16 // 默认16个分片
	}

	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 5 * time.Minute // 默认5分钟清理一次
	}

	cache := &Cache[K, V]{
		shards:     make([]*CacheShard[K, V], config.ShardCount),
		shardCount: config.ShardCount,
		hashFunc: func(key K) uint32 {
			// 简单的哈希函数，实际生产中应使用更好的哈希算法
			h := fmt.Sprintf("%v", key)
			var sum uint32
			for i := 0; i < len(h); i++ {
				sum += uint32(h[i])
			}
			return sum
		},
		config:  config,
		metrics: &CacheMetrics{},
	}

	// 初始化每个分片
	for i := 0; i < config.ShardCount; i++ {
		cache.shards[i] = &CacheShard[K, V]{
			items:       make(map[K]*Item[V]),
			lruList:     list.New(),
			lruItems:    make(map[K]*list.Element),
			lfuHeap:     NewLFUHeap[K](),
			fifoQueue:   list.New(),
			fifoItems:   make(map[K]*list.Element),
			policy:      config.EvictionPolicy,
			capacity:    config.MaxItems / config.ShardCount,
			memoryLimit: config.MaxMemory / int64(config.ShardCount),
		}

		if cache.shards[i].capacity <= 0 {
			cache.shards[i].capacity = 1000 // 默认每分片1000个项
		}

		// 初始化LFU堆
		heap.Init(cache.shards[i].lfuHeap)
	}

	// 启动定期清理过期项的协程
	go cache.startCleanup(config.CleanupInterval)

	return cache
}

// 启动定期清理
func (c *Cache[K, V]) startCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		c.cleanupExpired()
	}
}

// 清理过期项
func (c *Cache[K, V]) cleanupExpired() {
	now := time.Now()

	for i := 0; i < c.shardCount; i++ {
		shard := c.shards[i]
		shard.mutex.Lock()

		// 使用TTL堆快速找到过期项
		if shard.ttlHeap != nil {
			for shard.ttlHeap.Len() > 0 {
				item := shard.ttlHeap.items[0]
				if item.expireAt.After(now) {
					break
				}
				c.removeFromDataStructures(shard, item.key)
			}
		} else {
			// 如果没有TTL堆，使用传统方式清理
			var keysToDelete []K
			for k, item := range shard.items {
				if !item.expireAt.IsZero() && item.expireAt.Before(now) {
					keysToDelete = append(keysToDelete, k)
				}
			}
			shard.mutex.Unlock()

			// 删除过期项
			for _, k := range keysToDelete {
				c.Delete(k)
			}
			continue
		}

		// 衰减访问计数
		if shard.accessCounter != nil {
			shard.accessCounter.Decay()
		}

		shard.mutex.Unlock()
	}
}

// 获取分片索引
func (c *Cache[K, V]) getShard(key K) *CacheShard[K, V] {
	return c.shards[c.hashFunc(key)%uint32(c.shardCount)]
}

// 添加监听器
func (c *Cache[K, V]) AddListener(listener CacheListener[K, V]) {
	c.listeners = append(c.listeners, listener)
	for i := 0; i < c.shardCount; i++ {
		c.shards[i].listeners = append(c.shards[i].listeners, listener)
	}
}

// 添加远程缓存
func (c *Cache[K, V]) AddRemoteCache(remote RemoteCache[K, V]) {
	c.remoteCaches = append(c.remoteCaches, remote)
}

// 添加分布式对等节点
func (c *Cache[K, V]) AddPeer(peer DistributedPeer[K, V]) {
	c.peers = append(c.peers, peer)
}

// 添加回调函数
func (c *Cache[K, V]) AddFallback(fallback FallbackFunc[K, V]) {
	c.fallbacks = append(c.fallbacks, fallback)
}

// 设置缓存项
func (c *Cache[K, V]) Set(key K, value V, ttl time.Duration) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	size := estimateSize(value)

	// 检查是否已存在同键项目，如果有先删除并更新内存占用
	if oldItem, exists := shard.items[key]; exists {
		shard.currentSize -= int64(oldItem.size)
		// 从对应的数据结构中移除
		c.removeFromDataStructures(shard, key)
	}

	expireAt := time.Time{}
	if ttl > 0 {
		expireAt = time.Now().Add(ttl)
	}

	// 检查容量和内存限制
	if c.shouldEvict(shard) {
		c.evictItem(shard, EvictionReasonCapacity)
	}

	// 创建新项
	item := &Item[V]{
		value:        value,
		expireAt:     expireAt,
		lastAccessed: time.Now(),
		size:         size,
	}

	// 添加到缓存
	shard.items[key] = item
	shard.currentSize += int64(size)

	// 根据策略添加到相应数据结构
	switch shard.policy {
	case LRU:
		elem := shard.lruList.PushFront(key)
		shard.lruItems[key] = elem
	case LFU:
		heap.Push(shard.lfuHeap, &LFUItem[K]{key: key, count: 0})
	case FIFO:
		elem := shard.fifoQueue.PushBack(key)
		shard.fifoItems[key] = elem
	case TTL:
		if shard.ttlHeap == nil {
			shard.ttlHeap = NewTTLHeap[K]()
		}
		heap.Push(shard.ttlHeap, &TTLItem[K]{
			key:      key,
			expireAt: expireAt,
		})
	}

	// 同步到分布式节点
	for _, peer := range c.peers {
		go peer.SyncSet(key, value, ttl)
	}
}

// 批量设置
func (c *Cache[K, V]) SetMulti(items map[K]V, ttl time.Duration) {
	// 按分片分组
	shardItems := make([]map[K]V, c.shardCount)
	for i := 0; i < c.shardCount; i++ {
		shardItems[i] = make(map[K]V)
	}

	for k, v := range items {
		shardIndex := int(c.hashFunc(k) % uint32(c.shardCount))
		shardItems[shardIndex][k] = v
	}

	// 对每个分片进行批量设置
	for i, itemMap := range shardItems {
		if len(itemMap) == 0 {
			continue
		}

		shard := c.shards[i]
		shard.mutex.Lock()

		for k, v := range itemMap {
			size := estimateSize(v)

			// 检查是否已存在
			if oldItem, exists := shard.items[k]; exists {
				shard.currentSize -= int64(oldItem.size)
				c.removeFromDataStructures(shard, k)
			}

			// 检查容量和内存限制
			if c.shouldEvict(shard) {
				c.evictItem(shard, EvictionReasonCapacity)
			}

			expireAt := time.Time{}
			if ttl > 0 {
				expireAt = time.Now().Add(ttl)
			}

			// 创建新项
			item := &Item[V]{
				value:        v,
				expireAt:     expireAt,
				lastAccessed: time.Now(),
				size:         size,
			}

			// 添加到缓存
			shard.items[k] = item
			shard.currentSize += int64(size)

			// 根据策略添加到相应数据结构
			switch shard.policy {
			case LRU:
				elem := shard.lruList.PushFront(k)
				shard.lruItems[k] = elem
			case LFU:
				heap.Push(shard.lfuHeap, &LFUItem[K]{key: k, count: 0})
			case FIFO:
				elem := shard.fifoQueue.PushBack(k)
				shard.fifoItems[k] = elem
			}
		}

		shard.mutex.Unlock()
	}

	// 同步到分布式节点
	for _, peer := range c.peers {
		go peer.SyncSetMulti(items, ttl)
	}
}

// 获取缓存项
func (c *Cache[K, V]) Get(key K) (V, bool) {
	shard := c.getShard(key)
	shard.mutex.RLock()
	item, found := shard.items[key]
	shard.mutex.RUnlock()

	var value V
	var err error

	if found {
		// 检查是否过期
		if !item.expireAt.IsZero() && item.expireAt.Before(time.Now()) {
			// 已过期，删除
			c.Delete(key)
			found = false
		} else {
			// 更新访问信息
			c.updateAccessInfo(shard, key, item)

			// 检查是否需要重置TTL
			if item.resetInfo != nil {
				now := time.Now()
				info := item.resetInfo

				// 如果超过指定时间段，重置计数
				if now.Sub(info.startTime) > time.Duration(info.seconds)*time.Second {
					info.count = 1
					info.startTime = now
				} else {
					info.count++
					// 如果达到指定次数，重置TTL
					if info.count >= info.times {
						c.ResetTTL(key)
						info.count = 0
						info.startTime = now
					}
				}
			}

			c.metrics.mu.Lock()
			c.metrics.hits++
			c.metrics.mu.Unlock()

			value = item.value
			return value, true
		}
	}

	c.metrics.mu.Lock()
	c.metrics.misses++
	c.metrics.mu.Unlock()

	// 本地缓存未命中，尝试远程缓存
	for _, remote := range c.remoteCaches {
		if remoteValue, ok := remote.Get(key); ok {
			c.metrics.mu.Lock()
			c.metrics.remoteHits++
			c.metrics.mu.Unlock()

			// 将远程值设置到本地缓存
			if item != nil && !item.expireAt.IsZero() {
				ttl := time.Until(item.expireAt)
				c.Set(key, remoteValue, ttl)
			} else {
				c.Set(key, remoteValue, c.config.DefaultTTL)
			}

			return remoteValue, true
		}
	}

	c.metrics.mu.Lock()
	c.metrics.remoteMisses++
	c.metrics.mu.Unlock()

	// 远程缓存也未命中，尝试回调函数
	for _, fallback := range c.fallbacks {
		value, err = fallback(key)
		if err == nil {
			// 设置到本地和远程缓存
			c.Set(key, value, c.config.DefaultTTL)

			// 同步到远程缓存
			for _, remote := range c.remoteCaches {
				remote.Set(key, value, c.config.DefaultTTL)
			}

			c.metrics.mu.Lock()
			c.metrics.fallbackCalls++
			c.metrics.mu.Unlock()

			return value, true
		}
	}

	return value, false
}

// 批量获取
func (c *Cache[K, V]) GetMulti(keys []K) map[K]V {
	result := make(map[K]V)
	missingKeys := make([]K, 0)

	// 首先尝试从本地缓存获取
	for _, key := range keys {
		if val, found := c.Get(key); found {
			result[key] = val
		} else {
			missingKeys = append(missingKeys, key)
		}
	}

	// 如果有远程缓存，尝试批量获取缺失的键
	if len(missingKeys) > 0 && len(c.remoteCaches) > 0 {
		for _, remote := range c.remoteCaches {
			remoteResults := remote.GetMulti(missingKeys)

			// 将远程结果添加到结果集并缓存到本地
			for k, v := range remoteResults {
				result[k] = v
				c.Set(k, v, c.config.DefaultTTL)

				// 从缺失键列表中移除
				for i, mk := range missingKeys {
					if fmt.Sprintf("%v", mk) == fmt.Sprintf("%v", k) {
						missingKeys = append(missingKeys[:i], missingKeys[i+1:]...)
						break
					}
				}
			}

			// 如果没有缺失的键，退出循环
			if len(missingKeys) == 0 {
				break
			}
		}
	}

	// 如果仍有缺失的键，尝试使用回调函数
	if len(missingKeys) > 0 && len(c.fallbacks) > 0 {
		for _, key := range missingKeys {
			for _, fallback := range c.fallbacks {
				if val, err := fallback(key); err == nil {
					result[key] = val
					c.Set(key, val, c.config.DefaultTTL)

					// 同步到远程缓存
					for _, remote := range c.remoteCaches {
						remote.Set(key, val, c.config.DefaultTTL)
					}

					break
				}
			}
		}
	}

	return result
}

// 删除缓存项
func (c *Cache[K, V]) Delete(key K) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	if item, found := shard.items[key]; found {
		// 调用淘汰监听器
		for _, listener := range shard.listeners {
			listener.OnEviction(key, item.value, EvictionReasonManual)
		}

		// 移除项并更新内存使用量
		shard.currentSize -= int64(item.size)
		delete(shard.items, key)

		// 从对应的数据结构中移除
		c.removeFromDataStructures(shard, key)

		// 更新指标
		c.metrics.mu.Lock()
		c.metrics.evictions++
		c.metrics.memoryUsage = c.getCurrentMemoryUsage()
		c.metrics.mu.Unlock()

		// 同步删除操作到分布式节点
		for _, peer := range c.peers {
			go peer.SyncDelete(key)
		}

		// 同步删除到远程缓存
		for _, remote := range c.remoteCaches {
			go remote.Delete(key)
		}
	}
}

// 重置TTL
func (c *Cache[K, V]) ResetTTL(key K) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	if item, found := shard.items[key]; found && !item.expireAt.IsZero() {
		// 计算原TTL
		originalTTL := item.expireAt.Sub(item.lastAccessed)
		// 设置新过期时间
		newExpireAt := time.Now().Add(originalTTL)
		item.expireAt = newExpireAt

		// 更新TTL堆
		if shard.ttlHeap != nil {
			shard.ttlHeap.UpdateExpiry(key, newExpireAt)
		}
	}
}

// 设置访问次数重置TTL
func (c *Cache[K, V]) SetResetTTLOnAccess(key K, seconds int, times int) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	if item, found := shard.items[key]; found {
		item.resetInfo = &ResetTTLInfo{
			seconds:   seconds,
			times:     times,
			startTime: time.Now(),
			count:     0,
		}
	}
}

// 更新访问信息
func (c *Cache[K, V]) updateAccessInfo(shard *CacheShard[K, V], key K, item *Item[V]) {
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	item.lastAccessed = time.Now()
	item.accessCount++

	// 根据淘汰策略更新数据结构
	switch shard.policy {
	case LRU:
		if elem, ok := shard.lruItems[key]; ok {
			shard.lruList.MoveToFront(elem)
		}
	case LFU:
		if shard.lfuHeap != nil {
			shard.lfuHeap.UpdateCount(key, item.accessCount)
		}
	}
}

// 获取当前内存使用量
func (c *Cache[K, V]) getCurrentMemoryUsage() int64 {
	var total int64
	for i := 0; i < c.shardCount; i++ {
		shard := c.shards[i]
		shard.mutex.RLock()
		total += shard.currentSize
		shard.mutex.RUnlock()
	}
	return total
}

// 获取缓存指标
func (c *Cache[K, V]) GetMetrics() CacheMetricsSnapshot {
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()

	return CacheMetricsSnapshot{
		Hits:          c.metrics.hits,
		Misses:        c.metrics.misses,
		Evictions:     c.metrics.evictions,
		MemoryUsage:   c.metrics.memoryUsage,
		RemoteHits:    c.metrics.remoteHits,
		RemoteMisses:  c.metrics.remoteMisses,
		FallbackCalls: c.metrics.fallbackCalls,
		ItemCount:     c.getItemCount(),
	}
}

// 获取项目数量
func (c *Cache[K, V]) getItemCount() int {
	var count int
	for i := 0; i < c.shardCount; i++ {
		shard := c.shards[i]
		shard.mutex.RLock()
		count += len(shard.items)
		shard.mutex.RUnlock()
	}
	return count
}

// 缓存指标快照
type CacheMetricsSnapshot struct {
	Hits          int64
	Misses        int64
	Evictions     int64
	MemoryUsage   int64
	RemoteHits    int64
	RemoteMisses  int64
	FallbackCalls int64
	ItemCount     int
}

// 预估对象大小
func estimateSize(v interface{}) int {
	// 实际应用中，应该根据对象类型更精确地计算大小
	// 这里是简化的实现
	switch val := v.(type) {
	case string:
		return len(val)
	case []byte:
		return len(val)
	default:
		// 默认大小，实际应根据对象类型详细计算
		return 64
	}
}

// 缓存集合 - 支持多集缓存
type CacheCollection[K comparable, V any] struct {
	caches map[string]*Cache[K, V]
	mutex  sync.RWMutex
}

// 创建新的缓存集合
func NewCacheCollection[K comparable, V any]() *CacheCollection[K, V] {
	return &CacheCollection[K, V]{
		caches: make(map[string]*Cache[K, V]),
	}
}

// 添加缓存
func (cc *CacheCollection[K, V]) AddCache(name string, cache *Cache[K, V]) {
	cc.mutex.Lock()
	defer cc.mutex.Unlock()
	cc.caches[name] = cache
}

// 获取缓存
func (cc *CacheCollection[K, V]) GetCache(name string) (*Cache[K, V], bool) {
	cc.mutex.RLock()
	defer cc.mutex.RUnlock()
	cache, found := cc.caches[name]
	return cache, found
}

// 设置缓存项到所有缓存
func (cc *CacheCollection[K, V]) SetToAll(key K, value V, ttl time.Duration) {
	cc.mutex.RLock()
	defer cc.mutex.RUnlock()

	for _, cache := range cc.caches {
		cache.Set(key, value, ttl)
	}
}

// 从所有缓存中获取项
func (cc *CacheCollection[K, V]) GetFromAll(key K) (V, bool) {
	cc.mutex.RLock()
	defer cc.mutex.RUnlock()

	var lastValue V
	found := false

	// 尝试从每个缓存获取
	for _, cache := range cc.caches {
		if value, ok := cache.Get(key); ok {
			lastValue = value
			found = true
			// 不立即返回，继续检查其他缓存以更新访问信息
		}
	}

	return lastValue, found
}

// 从所有缓存中删除项
func (cc *CacheCollection[K, V]) DeleteFromAll(key K) {
	cc.mutex.RLock()
	defer cc.mutex.RUnlock()

	for _, cache := range cc.caches {
		cache.Delete(key)
	}
}

// Redis实现的远程缓存
type RedisCache[K comparable, V any] struct {
	// 这里应该包含Redis客户端
	// client redis.Client
	// 序列化和反序列化函数
	marshal   func(V) ([]byte, error)
	unmarshal func([]byte) (V, error)
}

// 创建Redis缓存
func NewRedisCache[K comparable, V any](
// redisClient redis.Client,
	marshal func(V) ([]byte, error),
	unmarshal func([]byte) (V, error),
) *RedisCache[K, V] {
	return &RedisCache[K, V]{
		// client: redisClient,
		marshal:   marshal,
		unmarshal: unmarshal,
	}
}

// Redis缓存Get实现
func (rc *RedisCache[K, V]) Get(key K) (V, bool) {
	var value V

	// 实际实现应该使用Redis客户端获取值
	// data, err := rc.client.Get(context.Background(), fmt.Sprintf("%v", key)).Bytes()
	// if err != nil {
	//     return value, false
	// }
	//
	// value, err = rc.unmarshal(data)
	// if err != nil {
	//     return value, false
	// }

	return value, false // 模拟实现
}

// Redis缓存Set实现
func (rc *RedisCache[K, V]) Set(key K, value V, ttl time.Duration) {
	// data, err := rc.marshal(value)
	// if err != nil {
	//     return
	// }
	//
	// rc.client.Set(context.Background(), fmt.Sprintf("%v", key), data, ttl)
}

// Redis缓存Delete实现
func (rc *RedisCache[K, V]) Delete(key K) {
	// rc.client.Del(context.Background(), fmt.Sprintf("%v", key))
}

// Redis缓存GetMulti实现
func (rc *RedisCache[K, V]) GetMulti(keys []K) map[K]V {
	result := make(map[K]V)

	// 实际实现应该使用Redis客户端的批量获取
	// pipeline := rc.client.Pipeline()
	// cmds := make(map[K]*redis.StringCmd)
	//
	// for _, key := range keys {
	//     cmds[key] = pipeline.Get(context.Background(), fmt.Sprintf("%v", key))
	// }
	//
	// _, _ = pipeline.Exec(context.Background())
	//
	// for key, cmd := range cmds {
	//     data, err := cmd.Bytes()
	//     if err != nil {
	//         continue
	//     }
	//
	//     value, err := rc.unmarshal(data)
	//     if err != nil {
	//         continue
	//     }
	//
	//     result[key] = value
	// }

	return result
}

// Redis缓存SetMulti实现
func (rc *RedisCache[K, V]) SetMulti(items map[K]V, ttl time.Duration) {
	// pipeline := rc.client.Pipeline()
	//
	// for key, value := range items {
	//     data, err := rc.marshal(value)
	//     if err != nil {
	//         continue
	//     }
	//
	//     pipeline.Set(context.Background(), fmt.Sprintf("%v", key), data, ttl)
	// }
	//
	// _, _ = pipeline.Exec(context.Background())
}

// HTTP实现的分布式同步
type HTTPPeer[K comparable, V any] struct {
	// 节点地址
	peerURL string
	// HTTP客户端
	//client http.Client
	// 序列化和反序列化函数
	marshal   func(V) ([]byte, error)
	unmarshal func([]byte) (V, error)
}

// 创建HTTP同步节点
func NewHTTPPeer[K comparable, V any](
	peerURL string,
	marshal func(V) ([]byte, error),
	unmarshal func([]byte) (V, error),
) *HTTPPeer[K, V] {
	return &HTTPPeer[K, V]{
		peerURL:   peerURL,
		marshal:   marshal,
		unmarshal: unmarshal,
	}
}

// HTTP同步Set实现
func (hp *HTTPPeer[K, V]) SyncSet(key K, value V, ttl time.Duration) {
	// 实际实现应该发送HTTP请求到对等节点
	// data, err := hp.marshal(value)
	// if err != nil {
	//     return
	// }
	//
	// req := &SyncRequest{
	//     Key:   fmt.Sprintf("%v", key),
	//     Value: data,
	//     TTL:   ttl.Milliseconds(),
	// }
	//
	// jsonData, _ := json.Marshal(req)
	// _, _ = hp.client.Post(hp.peerURL+"/sync/set", "application/json", bytes.NewReader(jsonData))
}

// HTTP同步Delete实现
func (hp *HTTPPeer[K, V]) SyncDelete(key K) {
	// 实际实现应该发送HTTP请求到对等节点
	// req := &SyncDeleteRequest{
	//     Key: fmt.Sprintf("%v", key),
	// }
	//
	// jsonData, _ := json.Marshal(req)
	// _, _ = hp.client.Post(hp.peerURL+"/sync/delete", "application/json", bytes.NewReader(jsonData))
}

// HTTP同步SetMulti实现
func (hp *HTTPPeer[K, V]) SyncSetMulti(items map[K]V, ttl time.Duration) {
	// 实际实现应该发送HTTP请求到对等节点
	// syncItems := make(map[string][]byte)
	//
	// for key, value := range items {
	//     data, err := hp.marshal(value)
	//     if err != nil {
	//         continue
	//     }
	//
	//     syncItems[fmt.Sprintf("%v", key)] = data
	// }
	//
	// req := &SyncMultiRequest{
	//     Items: syncItems,
	//     TTL:   ttl.Milliseconds(),
	// }
	//
	// jsonData, _ := json.Marshal(req)
	// _, _ = hp.client.Post(hp.peerURL+"/sync/setmulti", "application/json", bytes.NewReader(jsonData))
}

// 同步请求结构
type SyncRequest struct {
	Key   string `json:"key"`
	Value []byte `json:"value"`
	TTL   int64  `json:"ttl"`
}

// 同步删除请求结构
type SyncDeleteRequest struct {
	Key string `json:"key"`
}

// 同步批量请求结构
type SyncMultiRequest struct {
	Items map[string][]byte `json:"items"`
	TTL   int64             `json:"ttl"`
}

// 缓存工厂 - 创建预配置的缓存实例
type CacheFactory[K comparable, V any] struct {
	defaultConfig CacheConfig
}

// 创建缓存工厂
func NewCacheFactory[K comparable, V any](defaultConfig CacheConfig) *CacheFactory[K, V] {
	return &CacheFactory[K, V]{
		defaultConfig: defaultConfig,
	}
}

// 创建缓存
func (cf *CacheFactory[K, V]) CreateCache() *Cache[K, V] {
	return NewCache[K, V](cf.defaultConfig)
}

// 创建带监听器的缓存
func (cf *CacheFactory[K, V]) CreateCacheWithListener(listener CacheListener[K, V]) *Cache[K, V] {
	cache := NewCache[K, V](cf.defaultConfig)
	cache.AddListener(listener)
	return cache
}

// 创建带远程缓存的缓存
func (cf *CacheFactory[K, V]) CreateCacheWithRemote(remote RemoteCache[K, V]) *Cache[K, V] {
	cache := NewCache[K, V](cf.defaultConfig)
	cache.AddRemoteCache(remote)
	return cache
}

// 创建带分布式同步的缓存
func (cf *CacheFactory[K, V]) CreateCacheWithPeers(peers []DistributedPeer[K, V]) *Cache[K, V] {
	cache := NewCache[K, V](cf.defaultConfig)
	for _, peer := range peers {
		cache.AddPeer(peer)
	}
	return cache
}

// 创建带回调函数的缓存
func (cf *CacheFactory[K, V]) CreateCacheWithFallback(fallback FallbackFunc[K, V]) *Cache[K, V] {
	cache := NewCache[K, V](cf.defaultConfig)
	cache.AddFallback(fallback)
	return cache
}

// 简单的内存估算器
type SizeEstimator interface {
	EstimateSize(interface{}) int
}

// 默认大小估算器
type DefaultSizeEstimator struct{}

func (d *DefaultSizeEstimator) EstimateSize(v interface{}) int {
	return estimateSize(v)
}

// 缓存监听器实现
type EvictionLogger[K comparable, V any] struct {
	logFunc func(format string, args ...interface{})
}

// 创建淘汰日志记录器
func NewEvictionLogger[K comparable, V any](logFunc func(format string, args ...interface{})) *EvictionLogger[K, V] {
	return &EvictionLogger[K, V]{
		logFunc: logFunc,
	}
}

// 实现淘汰通知
func (el *EvictionLogger[K, V]) OnEviction(key K, value V, reason EvictionReason) {
	reasonStr := "未知"
	switch reason {
	case EvictionReasonExpired:
		reasonStr = "过期"
	case EvictionReasonCapacity:
		reasonStr = "容量限制"
	case EvictionReasonMemory:
		reasonStr = "内存限制"
	case EvictionReasonManual:
		reasonStr = "手动删除"
	}

	el.logFunc("缓存项被淘汰: 键=%v, 原因=%s", key, reasonStr)
}

// 多级缓存管理器
type MultiLevelCache[K comparable, V any] struct {
	localCache   *Cache[K, V]
	remoteCache  RemoteCache[K, V]
	fallbackFunc FallbackFunc[K, V]
}

// 创建多级缓存
func NewMultiLevelCache[K comparable, V any](
	localConfig CacheConfig,
	remoteCache RemoteCache[K, V],
	fallback FallbackFunc[K, V],
) *MultiLevelCache[K, V] {
	local := NewCache[K, V](localConfig)
	local.AddRemoteCache(remoteCache)
	local.AddFallback(fallback)

	return &MultiLevelCache[K, V]{
		localCache:   local,
		remoteCache:  remoteCache,
		fallbackFunc: fallback,
	}
}

// 获取值
func (mlc *MultiLevelCache[K, V]) Get(key K) (V, error) {
	// 尝试从本地缓存获取
	if value, found := mlc.localCache.Get(key); found {
		return value, nil
	}

	// 默认值
	var defaultValue V
	return defaultValue, fmt.Errorf("缓存中未找到键: %v", key)
}

// 设置值
func (mlc *MultiLevelCache[K, V]) Set(key K, value V, ttl time.Duration) {
	// 同时设置到本地和远程缓存
	mlc.localCache.Set(key, value, ttl)
	mlc.remoteCache.Set(key, value, ttl)
}

// 删除值
func (mlc *MultiLevelCache[K, V]) Delete(key K) {
	mlc.localCache.Delete(key)
	mlc.remoteCache.Delete(key)
}

// 预热缓存
func (mlc *MultiLevelCache[K, V]) WarmUp(keys []K) {
	for _, key := range keys {
		// 尝试从远程获取
		if value, found := mlc.remoteCache.Get(key); found {
			// 设置到本地缓存
			mlc.localCache.Set(key, value, mlc.localCache.config.DefaultTTL)
		} else if mlc.fallbackFunc != nil {
			// 尝试从回调函数获取
			if value, err := mlc.fallbackFunc(key); err == nil {
				mlc.Set(key, value, mlc.localCache.config.DefaultTTL)
			}
		}
	}
}

// 更多的淘汰策略实现

// 双LRU策略 - 优先考虑最近最少使用，但也考虑访问频率
type DoubleLRU[K comparable] struct {
	recent    *list.List          // 最近访问列表
	frequent  *list.List          // 频繁访问列表
	recentMap map[K]*list.Element // 最近访问映射
	freqMap   map[K]*list.Element // 频繁访问映射
	maxSize   int                 // 最大缓存大小
}

func NewDoubleLRU[K comparable](maxSize int) *DoubleLRU[K] {
	return &DoubleLRU[K]{
		recent:    list.New(),
		frequent:  list.New(),
		recentMap: make(map[K]*list.Element),
		freqMap:   make(map[K]*list.Element),
		maxSize:   maxSize,
	}
}

// TinyLFU策略 - 使用计数器矩阵跟踪频率

// W-TinyLFU策略 - 加权窗口TinyLFU

// 分层缓存策略 - 不同层级使用不同策略

// 记忆式淘汰 - 记录已淘汰项的键，以便可能的重新使用

// 适应性策略 - 根据访问模式自动调整淘汰策略

// 更多的监控指标
type ExtendedMetrics struct {
	HitRate           float64       // 命中率
	EvictionRate      float64       // 淘汰率
	AverageAccessTime time.Duration // 平均访问时间
	LockContentions   int64         // 锁竞争次数
	ShardDistribution []int         // 分片分布
}

// 收集扩展指标
func (c *Cache[K, V]) CollectExtendedMetrics() ExtendedMetrics {
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()

	totalAccess := c.metrics.hits + c.metrics.misses
	hitRate := float64(0)
	if totalAccess > 0 {
		hitRate = float64(c.metrics.hits) / float64(totalAccess)
	}

	evictionRate := float64(0)
	if totalAccess > 0 {
		evictionRate = float64(c.metrics.evictions) / float64(totalAccess)
	}

	// 收集分片分布
	shardDist := make([]int, c.shardCount)
	for i := 0; i < c.shardCount; i++ {
		shard := c.shards[i]
		shard.mutex.RLock()
		shardDist[i] = len(shard.items)
		shard.mutex.RUnlock()
	}

	return ExtendedMetrics{
		HitRate:           hitRate,
		EvictionRate:      evictionRate,
		ShardDistribution: shardDist,
	}
}

// 示例用法
func Example() {
	// 创建缓存配置
	config := CacheConfig{
		ShardCount:      32,
		EvictionPolicy:  LRU,
		DefaultTTL:      10 * time.Minute,
		MaxMemory:       100 * 1024 * 1024, // 100MB
		MaxItems:        10000,
		CleanupInterval: 5 * time.Minute,
	}

	// 创建缓存
	cache := NewCache[string, []byte](config)

	// 添加监听器
	logger := NewEvictionLogger[string, []byte](func(format string, args ...interface{}) {
		fmt.Printf(format+"\n", args...)
	})
	cache.AddListener(logger)

	// 添加回调函数
	cache.AddFallback(func(key string) ([]byte, error) {
		// 模拟从数据库获取数据
		return []byte("从数据库获取的值: " + key), nil
	})

	// 设置缓存项
	cache.Set("key1", []byte("value1"), 1*time.Minute)

	// 设置访问次数重置TTL
	cache.SetResetTTLOnAccess("key1", 30, 5) // 30秒内访问5次，重置TTL

	// 获取缓存项
	if value, found := cache.Get("key1"); found {
		fmt.Printf("缓存命中: %s\n", value)
	}

	// 获取缓存指标
	metrics := cache.GetMetrics()
	fmt.Printf("缓存命中: %d, 未命中: %d, 内存使用: %d bytes\n",
		metrics.Hits, metrics.Misses, metrics.MemoryUsage)

	// 批量操作
	keys := []string{"key1", "key2", "key3"}
	values := cache.GetMulti(keys)
	for k, v := range values {
		fmt.Printf("批量获取: %s = %s\n", k, v)
	}

	// 创建多集缓存
	collection := NewCacheCollection[string, []byte]()
	collection.AddCache("cache1", cache)

	// 创建第二个缓存
	cache2 := NewCache[string, []byte](config)
	collection.AddCache("cache2", cache2)

	// 在所有缓存中设置值
	collection.SetToAll("shared_key", []byte("shared_value"), 5*time.Minute)

	// 创建多级缓存
	redisCache := NewRedisCache[string, []byte](
		func(v []byte) ([]byte, error) { return v, nil },
		func(data []byte) ([]byte, error) { return data, nil },
	)

	multiLevel := NewMultiLevelCache[string, []byte](
		config,
		redisCache,
		func(key string) ([]byte, error) {
			return []byte("fallback value"), nil
		},
	)

	// 使用多级缓存
	multiLevel.Set("multi_key", []byte("multi_value"), 10*time.Minute)
}

// 判断是否需要淘汰
func (c *Cache[K, V]) shouldEvict(shard *CacheShard[K, V]) bool {
	// 检查容量限制
	if shard.capacity > 0 && len(shard.items) >= shard.capacity {
		return true
	}

	// 检查内存限制
	if shard.memoryLimit > 0 && shard.currentSize >= shard.memoryLimit {
		return true
	}

	return false
}

// 淘汰项目
func (c *Cache[K, V]) evictItem(shard *CacheShard[K, V], reason EvictionReason) {
	var keyToEvict K
	var found bool

	// 根据淘汰策略选择要淘汰的项
	switch shard.policy {
	case LRU:
		// 最近最少使用
		if shard.lruList.Len() > 0 {
			elem := shard.lruList.Back()
			keyToEvict, _ = elem.Value.(K)
			found = true
		}
	case LFU:
		// 最不常用
		if shard.lfuHeap.Len() > 0 {
			lfuItem := heap.Pop(shard.lfuHeap).(LFUItem[K])
			keyToEvict = lfuItem.key
			found = true
		}
	case FIFO:
		// 先进先出
		if shard.fifoQueue.Len() > 0 {
			elem := shard.fifoQueue.Front()
			keyToEvict, _ = elem.Value.(K)
			found = true
		}
	case Random:
		// 随机淘汰
		keys := make([]K, 0, len(shard.items))
		for k := range shard.items {
			keys = append(keys, k)
		}
		if len(keys) > 0 {
			keyToEvict = keys[rand.Intn(len(keys))]
			found = true
		}
	case TTL:
		// 基于过期时间，淘汰最早过期的
		var earliestExpiry time.Time
		for k, item := range shard.items {
			if !item.expireAt.IsZero() && (earliestExpiry.IsZero() || item.expireAt.Before(earliestExpiry)) {
				earliestExpiry = item.expireAt
				keyToEvict = k
				found = true
			}
		}

		// 如果没有过期项，随机选择
		if !found {
			keys := make([]K, 0, len(shard.items))
			for k := range shard.items {
				keys = append(keys, k)
			}
			if len(keys) > 0 {
				keyToEvict = keys[rand.Intn(len(keys))]
				found = true
			}
		}
	}

	// 执行淘汰
	if found {
		if item, ok := shard.items[keyToEvict]; ok {
			// 调用监听器
			for _, listener := range shard.listeners {
				listener.OnEviction(keyToEvict, item.value, reason)
			}

			// 移除项
			shard.currentSize -= int64(item.size)
			delete(shard.items, keyToEvict)

			// 从数据结构中移除
			c.removeFromDataStructures(shard, keyToEvict)

			// 更新指标
			c.metrics.mu.Lock()
			c.metrics.evictions++
			c.metrics.memoryUsage = c.getCurrentMemoryUsage()
			c.metrics.mu.Unlock()
		}
	}
}

// 从数据结构中移除项
func (c *Cache[K, V]) removeFromDataStructures(shard *CacheShard[K, V], key K) {
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	// 从LRU链表中移除
	if elem, ok := shard.lruItems[key]; ok {
		shard.lruList.Remove(elem)
		delete(shard.lruItems, key)
	}

	// 从FIFO队列中移除
	if elem, ok := shard.fifoItems[key]; ok {
		shard.fifoQueue.Remove(elem)
		delete(shard.fifoItems, key)
	}

	// 从LFU堆中移除
	if shard.lfuHeap != nil {
		shard.lfuHeap.Remove(key)
	}

	// 从TTL堆中移除
	if shard.ttlHeap != nil {
		shard.ttlHeap.Remove(key)
	}

	// 从访问计数器中移除
	if shard.accessCounter != nil {
		shard.accessCounter.Remove(key)
	}

	// 从布隆过滤器中移除（注意：布隆过滤器不支持删除，这里只是记录）
	if shard.bloomFilter != nil {
		// 布隆过滤器不支持删除操作，这里可以记录删除的键
		shard.deletedKeys[key] = struct{}{}
	}

	// 更新统计信息
	c.metrics.mu.Lock()
	c.metrics.evictions++
	c.metrics.mu.Unlock()

	// 触发清理事件
	if item, ok := shard.items[key]; ok {
		for _, listener := range shard.listeners {
			listener.OnEviction(key, item.value, EvictionReasonManual)
		}
	}

	// 清理相关元数据
	delete(shard.items, key)
	delete(shard.deletedKeys, key)
}

// 批量移除优化
func (c *Cache[K, V]) removeFromDataStructuresBatch(shard *CacheShard[K, V], keys []K) {
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	// 批量处理LRU
	for _, key := range keys {
		if elem, ok := shard.lruItems[key]; ok {
			shard.lruList.Remove(elem)
			delete(shard.lruItems, key)
		}
	}

	// 批量处理FIFO
	for _, key := range keys {
		if elem, ok := shard.fifoItems[key]; ok {
			shard.fifoQueue.Remove(elem)
			delete(shard.fifoItems, key)
		}
	}

	// 批量处理LFU
	if shard.lfuHeap != nil {
		for _, key := range keys {
			shard.lfuHeap.Remove(key)
		}
	}

	// 批量更新统计信息
	c.metrics.mu.Lock()
	c.metrics.evictions += int64(len(keys))
	c.metrics.mu.Unlock()

	// 批量触发清理事件
	for _, key := range keys {
		if item, ok := shard.items[key]; ok {
			for _, listener := range shard.listeners {
				listener.OnEviction(key, item.value, EvictionReasonManual)
			}
		}
	}
}

// 对象池 - 用于减少内存分配
type ItemPool[V any] struct {
	pool sync.Pool
}

func NewItemPool[V any]() *ItemPool[V] {
	return &ItemPool[V]{
		pool: sync.Pool{
			New: func() interface{} {
				return &Item[V]{}
			},
		},
	}
}

func (p *ItemPool[V]) Get() *Item[V] {
	return p.pool.Get().(*Item[V])
}

func (p *ItemPool[V]) Put(item *Item[V]) {
	// 重置项的状态
	item.value = *new(V)
	item.expireAt = time.Time{}
	item.lastAccessed = time.Time{}
	item.accessCount = 0
	item.size = 0
	item.resetInfo = nil
	p.pool.Put(item)
}

// 批量操作优化
type BatchOperation[K comparable, V any] struct {
	operations []Operation[K, V]
	mu         sync.Mutex
}

type Operation[K comparable, V any] struct {
	op    string // "set", "delete"
	key   K
	value V
	ttl   time.Duration
}

func NewBatchOperation[K comparable, V any]() *BatchOperation[K, V] {
	return &BatchOperation[K, V]{
		operations: make([]Operation[K, V], 0, 1000),
	}
}

func (bo *BatchOperation[K, V]) AddSet(key K, value V, ttl time.Duration) {
	bo.mu.Lock()
	defer bo.mu.Unlock()
	bo.operations = append(bo.operations, Operation[K, V]{
		op:    "set",
		key:   key,
		value: value,
		ttl:   ttl,
	})
}

func (bo *BatchOperation[K, V]) AddDelete(key K) {
	bo.mu.Lock()
	defer bo.mu.Unlock()
	bo.operations = append(bo.operations, Operation[K, V]{
		op:  "delete",
		key: key,
	})
}

func (bo *BatchOperation[K, V]) Execute(cache *Cache[K, V]) {
	// 按分片分组操作
	shardOps := make([]map[K]Operation[K, V], cache.shardCount)
	for i := 0; i < cache.shardCount; i++ {
		shardOps[i] = make(map[K]Operation[K, V])
	}

	// 分组操作
	for _, op := range bo.operations {
		shardIndex := int(cache.hashFunc(op.key) % uint32(cache.shardCount))
		shardOps[shardIndex][op.key] = op
	}

	// 并行执行每个分片的操作
	var wg sync.WaitGroup
	for i := 0; i < cache.shardCount; i++ {
		if len(shardOps[i]) == 0 {
			continue
		}

		wg.Add(1)
		go func(shardIndex int, ops map[K]Operation[K, V]) {
			defer wg.Done()
			shard := cache.shards[shardIndex]
			shard.mutex.Lock()
			defer shard.mutex.Unlock()

			for _, op := range ops {
				switch op.op {
				case "set":
					cache.setInternal(shard, op.key, op.value, op.ttl)
				case "delete":
					cache.deleteInternal(shard, op.key)
				}
			}
		}(i, shardOps[i])
	}
	wg.Wait()
}

// 内部设置方法
func (c *Cache[K, V]) setInternal(shard *CacheShard[K, V], key K, value V, ttl time.Duration) {
	size := estimateSize(value)

	if oldItem, exists := shard.items[key]; exists {
		shard.currentSize -= int64(oldItem.size)
		c.removeFromDataStructures(shard, key)
	}

	expireAt := time.Time{}
	if ttl > 0 {
		expireAt = time.Now().Add(ttl)
	}

	if c.shouldEvict(shard) {
		c.evictItem(shard, EvictionReasonCapacity)
	}

	item := &Item[V]{
		value:        value,
		expireAt:     expireAt,
		lastAccessed: time.Now(),
		size:         size,
	}

	shard.items[key] = item
	shard.currentSize += int64(size)

	switch shard.policy {
	case LRU:
		elem := shard.lruList.PushFront(key)
		shard.lruItems[key] = elem
	case LFU:
		heap.Push(shard.lfuHeap, &LFUItem[K]{key: key, count: 0})
	case FIFO:
		elem := shard.fifoQueue.PushBack(key)
		shard.fifoItems[key] = elem
	}
}

// 内部删除方法
func (c *Cache[K, V]) deleteInternal(shard *CacheShard[K, V], key K) {
	if item, found := shard.items[key]; found {
		for _, listener := range shard.listeners {
			listener.OnEviction(key, item.value, EvictionReasonManual)
		}

		shard.currentSize -= int64(item.size)
		delete(shard.items, key)
		c.removeFromDataStructures(shard, key)

		c.metrics.mu.Lock()
		c.metrics.evictions++
		c.metrics.memoryUsage = c.getCurrentMemoryUsage()
		c.metrics.mu.Unlock()
	}
}

// 内存优化：使用压缩存储
type CompressedItem[V any] struct {
	value        V
	expireAt     int64 // Unix timestamp
	lastAccessed int64 // Unix timestamp
	accessCount  uint32
	size         uint32
}

func compressItem[V any](item *Item[V]) *CompressedItem[V] {
	return &CompressedItem[V]{
		value:        item.value,
		expireAt:     item.expireAt.Unix(),
		lastAccessed: item.lastAccessed.Unix(),
		accessCount:  uint32(item.accessCount),
		size:         uint32(item.size),
	}
}

func decompressItem[V any](compressed *CompressedItem[V]) *Item[V] {
	return &Item[V]{
		value:        compressed.value,
		expireAt:     time.Unix(compressed.expireAt, 0),
		lastAccessed: time.Unix(compressed.lastAccessed, 0),
		accessCount:  int(compressed.accessCount),
		size:         int(compressed.size),
	}
}

// 使用示例
func ExampleWithOptimizations() {
	config := CacheConfig{
		ShardCount:      32,
		EvictionPolicy:  LRU,
		DefaultTTL:      10 * time.Minute,
		MaxMemory:       100 * 1024 * 1024,
		MaxItems:        10000,
		CleanupInterval: 5 * time.Minute,
	}

	cache := NewCache[string, []byte](config)
	itemPool := NewItemPool[[]byte]()
	batchOp := NewBatchOperation[string, []byte]()

	// 批量操作示例
	batchOp.AddSet("key1", []byte("value1"), 1*time.Minute)
	batchOp.AddSet("key2", []byte("value2"), 1*time.Minute)
	batchOp.AddDelete("key3")
	batchOp.Execute(cache)

	// 使用对象池
	item := itemPool.Get()
	item.value = []byte("pooled value")
	item.expireAt = time.Now().Add(1 * time.Hour)
	item.size = len(item.value)
	cache.Set("pooled_key", item.value, 1*time.Hour)
	itemPool.Put(item)
}

package mutil_level_cache

import (
	"container/heap"
	"container/list"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Item 缓存项结构
type Item[V any] struct {
	value        V             // 值
	expireAt     time.Time     // 过期时间
	lastAccessed time.Time     // 最后访问时间
	accessCount  int           // 访问次数
	size         int           // 内存占用大小(字节)
	resetInfo    *ResetTTLInfo // 访问次数重置信息
}

// ResetTTLInfo TTL重置信息
type ResetTTLInfo struct {
	seconds   int       // N秒内
	times     int       // 访问M次
	startTime time.Time // 开始时间
	count     int       // 当前计数
}

// CacheShard 缓存分片 - 减少锁竞争
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
	tinyLFU       *TinyLFUCounter // TinyLFU计数器
	doubleLRU     *DoubleLRU[K]   // 双LRU实现
	wTinyLFU      *WTinyLFU[K]    // 加权窗口TinyLFU实现
}

// shouldEvict 判断是否需要淘汰
func (s *CacheShard[K, V]) shouldEvict() bool {
	// 检查容量限制
	if s.capacity > 0 && len(s.items) >= s.capacity {
		return true
	}

	// 检查内存限制
	if s.memoryLimit > 0 && s.currentSize >= s.memoryLimit {
		return true
	}

	return false
}

// 缓存结构体
type Cache[K comparable, V any] struct {
	shards       []*CacheShard[K, V]     // 分片数组
	shardCount   int                     // 分片数量
	hashFunc     func(K) uint32          // 哈希函数
	config       CacheConfig             // 配置
	remoteCaches []RemoteCache[K, V]     // 远程缓存列表
	peers        []DistributedPeer[K, V] // 分布式节点列表
	fallbacks    []FallbackLoader[K, V]  // 回调函数列表
	listeners    []CacheListener[K, V]   // 监听器列表
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
		config: config,
	}

	// 初始化每个分片
	for i := 0; i < config.ShardCount; i++ {
		shard := &CacheShard[K, V]{
			items:       make(map[K]*Item[V]),
			policy:      config.EvictionPolicy,
			capacity:    config.MaxItems / config.ShardCount,
			memoryLimit: config.MaxMemory / int64(config.ShardCount),
		}

		if shard.capacity <= 0 {
			shard.capacity = 1000 // 默认每分片1000个项
		}

		// 根据策略初始化相应的数据结构
		switch config.EvictionPolicy {
		case LRU:
			shard.lruList = list.New()
			shard.lruItems = make(map[K]*list.Element)
		case LFU:
			shard.lfuHeap = NewLFUHeap[K]()
			heap.Init(shard.lfuHeap)
		case FIFO:
			shard.fifoQueue = list.New()
			shard.fifoItems = make(map[K]*list.Element)
		case TinyLFU:
			// 初始化TinyLFU计数器，使用分片容量的2倍作为计数器大小
			shard.tinyLFU = NewTinyLFUCounter(shard.capacity*2, 1000) // 1000次访问后衰减
		case DoubleLRULFU:
			// 初始化双LRU，使用分片容量
			shard.doubleLRU = NewDoubleLRU[K](shard.capacity)
		case SWTinyLFU:
			// 初始化加权窗口TinyLFU，使用分片容量作为窗口大小
			shard.wTinyLFU = NewWTinyLFU[K](shard.capacity)
		}
		// 初始化LFU堆
		cache.shards[i] = shard
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

// cleanupExpired 清理过期项
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
		}

		// 衰减访问计数
		if shard.accessCounter != nil {
			shard.accessCounter.Decay()
		}

		shard.mutex.Unlock()
	}
}

// removeFromDataStructures 从数据结构中移除项
func (c *Cache[K, V]) removeFromDataStructures(shard *CacheShard[K, V], key K) {
	// 从LRU链表中移除
	if shard.lruItems != nil {
		if elem, ok := shard.lruItems[key]; ok {
			shard.lruList.Remove(elem)
			delete(shard.lruItems, key)
		}
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

	// 从TinyLFU中移除
	if shard.tinyLFU != nil {
		// TinyLFU不需要显式移除，因为它是基于哈希的
	}

	// 从双LRU中移除
	if shard.doubleLRU != nil {
		shard.doubleLRU.Remove(key)
	}

	// 从布隆过滤器中移除（注意：布隆过滤器不支持删除，这里只是记录）
	if shard.bloomFilter != nil {
		// 布隆过滤器不支持删除操作，这里可以记录删除的键
		shard.deletedKeys[key] = struct{}{}
	}

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

// getShard 获取分片索引
func (c *Cache[K, V]) getShard(key K) *CacheShard[K, V] {
	return c.shards[c.hashFunc(key)%uint32(c.shardCount)]
}

// AddListener 添加监听器 TODO 同时支持 config 初始化 option
func (c *Cache[K, V]) AddListener(listener CacheListener[K, V]) {
	c.listeners = append(c.listeners, listener)
	for i := 0; i < c.shardCount; i++ {
		c.shards[i].listeners = append(c.shards[i].listeners, listener)
	}
}

// AddRemoteCache 添加远程缓存 TODO 同时支持 config 初始化 option
func (c *Cache[K, V]) AddRemoteCache(remote RemoteCache[K, V]) {
	c.remoteCaches = append(c.remoteCaches, remote)
}

// AddPeer 添加分布式对等节点 TODO 同时支持 config 初始化 option
func (c *Cache[K, V]) AddPeer(peer DistributedPeer[K, V]) {
	c.peers = append(c.peers, peer)
}

// AddFallback 添加回调函数 TODO 同时支持 config 初始化 option
func (c *Cache[K, V]) AddFallback(fallback FallbackLoader[K, V]) {
	c.fallbacks = append(c.fallbacks, fallback)
}

// Set 设置缓存项
func (c *Cache[K, V]) Set(key K, value V, ttl time.Duration) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer func() {
		shard.mutex.Unlock()
	}()

	size := EstimateSize(value)

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
	if shard.shouldEvict() {
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
		if !expireAt.IsZero() {
			heap.Push(shard.ttlHeap, &TTLItem[K]{
				key:      key,
				expireAt: expireAt,
			})
		}
	case TinyLFU:
		if shard.tinyLFU != nil {
			// 计算key的哈希值并更新TinyLFU计数器
			hash := c.hashFunc(key)
			shard.tinyLFU.Increment(hash)
		}
	case DoubleLRULFU:
		if shard.doubleLRU != nil {
			// 使用双LRU策略
			shard.doubleLRU.Access(key)

			// 同时更新LFU计数
			if shard.lfuHeap != nil {
				heap.Push(shard.lfuHeap, &LFUItem[K]{key: key, count: 0})
			}
		}
	case SWTinyLFU:
		if shard.wTinyLFU != nil {
			shard.wTinyLFU.Access(key, 0)
		}
	}

	// 同步到分布式节点
	for _, peer := range c.peers {
		go peer.SyncSet(key, value, ttl)
	}
}

// evictItem 淘汰项目
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
			lfuItem := heap.Pop(shard.lfuHeap).(*LFUItem[K])
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
		// 基于过期时间，使用TTL堆快速找到最早过期的项
		if shard.ttlHeap != nil && shard.ttlHeap.Len() > 0 {
			ttlItem := heap.Pop(shard.ttlHeap).(*TTLItem[K])
			keyToEvict = ttlItem.key
			found = true
		} else {
			// 如果没有TTL堆或堆为空，随机选择一个项
			keys := make([]K, 0, len(shard.items))
			for k := range shard.items {
				keys = append(keys, k)
			}
			if len(keys) > 0 {
				keyToEvict = keys[rand.Intn(len(keys))]
				found = true
			}
		}
	case TinyLFU:
		// 使用TinyLFU策略
		if shard.tinyLFU != nil {
			var minFreq uint8 = 255
			for k := range shard.items {
				hash := c.hashFunc(k)
				freq := shard.tinyLFU.Get(hash)
				if freq < minFreq {
					minFreq = freq
					keyToEvict = k
					found = true
				}
			}
		}
	case DoubleLRULFU:
		// 使用双LRU+LFU混合策略
		if shard.doubleLRU != nil && shard.lfuHeap != nil {
			// 首先尝试从双LRU中获取候选
			keyToEvict, found = shard.doubleLRU.GetEvictionCandidate()

			// 如果找到候选，检查其LFU计数
			if found {
				// 获取当前项的LFU计数
				currentCount := 0
				if index, ok := shard.lfuHeap.indexMap[keyToEvict]; ok {
					currentCount = shard.lfuHeap.items[index].count
				}

				// 如果LFU计数较高，尝试找到LFU计数更低的项
				if currentCount > 1 {
					// 遍历所有项，找到LFU计数最低的项
					minCount := currentCount
					for k := range shard.items {
						if index, ok := shard.lfuHeap.indexMap[k]; ok {
							count := shard.lfuHeap.items[index].count
							if count < minCount {
								minCount = count
								keyToEvict = k
							}
						}
					}
				}
			}
		}
	case SWTinyLFU:
		// 使用加权窗口TinyLFU策略
		if shard.wTinyLFU != nil {
			keyToEvict, found = shard.wTinyLFU.GetEvictionCandidate()
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

			// 从数据结构中移除
			c.removeFromDataStructures(shard, keyToEvict)
		}
	}
}

// getCurrentMemoryUsage 获取当前内存使用量
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

// Get 获取缓存项
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
			value = item.value
			return value, true
		}
	}

	// 本地缓存未命中，尝试远程缓存
	for _, remote := range c.remoteCaches {
		if remoteValue, ok := remote.Get(key); ok {
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

	// 远程缓存也未命中，尝试回调函数
	for _, fallback := range c.fallbacks {
		value, err = fallback.Load(key)
		if err == nil {
			// 设置到本地和远程缓存
			c.Set(key, value, c.config.DefaultTTL)
			// 同步到远程缓存
			for _, remote := range c.remoteCaches {
				remote.Set(key, value, c.config.DefaultTTL)
			}
			return value, true
		}
	}

	return value, false
}

// Delete 删除缓存项
func (c *Cache[K, V]) Delete(key K) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer func() {
		shard.mutex.Unlock()
	}()

	if item, found := shard.items[key]; found {
		// 调用淘汰监听器
		for _, listener := range shard.listeners {
			listener.OnEviction(key, item.value, EvictionReasonManual)
		}

		// 移除项并更新内存使用量
		shard.currentSize -= int64(item.size)

		// 从对应的数据结构中移除
		c.removeFromDataStructures(shard, key)

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

// updateAccessInfo 更新访问信息
func (c *Cache[K, V]) updateAccessInfo(shard *CacheShard[K, V], key K, item *Item[V]) {
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	// item 数据更新
	item.lastAccessed = time.Now()
	item.accessCount++

	// 根据淘汰策略更新数据结构
	switch shard.policy {
	case LRU:
		// LRU策略：将访问的项移到链表前端
		if elem, ok := shard.lruItems[key]; ok {
			shard.lruList.MoveToFront(elem)
		}
	case LFU:
		// LFU策略：更新访问计数
		if shard.lfuHeap != nil {
			shard.lfuHeap.UpdateCount(key, item.accessCount)
		}
	case FIFO:
		// FIFO策略：不需要更新访问信息，因为FIFO只关心插入顺序
	case Random:
		// Random策略：不需要更新访问信息，因为随机淘汰不依赖访问信息
	case TTL:
		// TTL策略：更新过期时间
		if shard.ttlHeap != nil {
			shard.ttlHeap.UpdateExpiry(key, item.expireAt)
		}
	case TinyLFU:
		// TinyLFU策略：更新访问频率计数
		if shard.tinyLFU != nil {
			hash := c.hashFunc(key)
			shard.tinyLFU.Increment(hash)
		}
	case DoubleLRULFU:
		// DoubleLRULFU策略：同时更新LRU和LFU信息
		if shard.doubleLRU != nil {
			// 更新双LRU访问顺序
			shard.doubleLRU.Access(key)

			// 同时更新LFU计数
			if shard.lfuHeap != nil {
				shard.lfuHeap.UpdateCount(key, item.accessCount)
			}
		}
	case SWTinyLFU:
		// 加权窗口TinyLFU策略：更新滑动窗口中的访问频率
		if shard.wTinyLFU != nil {
			shard.wTinyLFU.Access(key, item.accessCount)
		}
	}
}

// 批量获取优化
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
		for _, fallback := range c.fallbacks {
			// 调用回调函数获取缺失的键
			if vals, err := fallback.BatchLoad(missingKeys); err == nil {
				for key, val := range vals {
					result[key] = val
					c.Set(key, val, c.config.DefaultTTL)
				}
			}
		}
	}

	return result
}

// ResetTTL 重置TTL 一段时间内访问次数达到指定次数，则重置TTL
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

func (c *Cache[K, V]) Refresh(key K) (value V, err error) {
	for _, fb := range c.fallbacks {
		value, err = fb.Load(key)
		if err != nil {
			return
		}
		c.Set(key, value, c.config.DefaultTTL)

		go func() {
			// 更新remote 缓存
			for _, remote := range c.remoteCaches {
				remote.Set(key, value, c.config.DefaultTTL)
			}
		}()

		go func() {
			for _, peer := range c.peers {
				peer.SyncSet(key, value, c.config.DefaultTTL)
			}
		}()

	}
	return
}

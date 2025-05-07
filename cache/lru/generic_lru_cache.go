package lru

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

/**
1. lru 淘汰方式
2. 支持设置淘汰时间，超时自动淘汰。
3. 支持在N秒内，访问M次，重置超时时间。
4. 支持设置内存上限
5. 支持设置数据项数量。
6. 支持淘汰数据通知。
7. 支持分布式同步
8.内存中储存的map 使用分片进行保存，减少锁的粒度，同时锁也是分段锁
9. key 和value支持范型
10.支持批量获取
11.支持多集缓存
12.支持fallback，如果cache中不存在则请求fallback函数
13. fallback 支持多集缓存，如果local cache 不存在，先fallback到远程cache，如果远程cache也不存在，则返回fallback函数
14. 支持多点fallback，功能同12，和13
使用golang 实现
*/

// CacheItem 表示缓存中的一个项目
type CacheItem[K comparable, V any] struct {
	Key             K         // 键
	Value           V         // 值
	ExpireTime      time.Time // 过期时间
	AccessCount     int       // 访问计数
	AccessCountTime time.Time // 访问计数重置时间
	Size            int64     // 数据大小（字节）
}

// EvictCallback 淘汰回调函数类型
type EvictCallback[K comparable, V any] func(key K, value V)

// SyncProvider 分布式同步接口
type SyncProvider[K comparable, V any] interface {
	// Publish 将本地缓存的更改发布到其他节点
	Publish(event SyncEvent[K, V]) error
	// Subscribe 订阅其他节点的缓存更改
	Subscribe(handler func(event SyncEvent[K, V])) error
}

// SyncEventType 同步事件类型
type SyncEventType int

const (
	SyncEventSet SyncEventType = iota
	SyncEventDelete
)

// SyncEvent 表示一个同步事件
type SyncEvent[K comparable, V any] struct {
	Type  SyncEventType
	Key   K
	Value V
	TTL   time.Duration
}

// FallbackFunc 数据获取回退函数类型
type FallbackFunc[K comparable, V any] func(key K) (V, error)

// BatchFallbackFunc 批量数据获取回退函数类型
type BatchFallbackFunc[K comparable, V any] func(keys []K) (map[K]V, error)

// ShardedCache 分片缓存结构体
type ShardedCache[K comparable, V any] struct {
	shards          []*cacheShard[K, V]
	shardCount      int
	ttl             time.Duration
	maxItems        int
	maxMemory       int64
	syncProvider    SyncProvider[K, V]
	onEvict         EvictCallback[K, V]
	accessThreshold int
	accessWindow    time.Duration
	currentMemory   int64
	ctx             context.Context
	cancel          context.CancelFunc
	hashFunc        func(K) uint32
	fallback        FallbackFunc[K, V]
	batchFallback   BatchFallbackFunc[K, V]
}

// cacheShard 表示缓存的一个分片
type cacheShard[K comparable, V any] struct {
	items        map[K]*list.Element
	lruList      *list.List
	mutex        sync.RWMutex
	currentItems int
	currentSize  int64
}

// Option 缓存配置选项函数类型
type Option[K comparable, V any] func(*ShardedCache[K, V])

// NewShardedCache 创建一个新的分片缓存
func NewShardedCache[K comparable, V any](options ...Option[K, V]) *ShardedCache[K, V] {
	ctx, cancel := context.WithCancel(context.Background())
	cache := &ShardedCache[K, V]{
		shardCount:      16, // 默认16分片
		ttl:             time.Hour,
		maxItems:        100000,
		maxMemory:       1 << 30, // 默认1GB
		accessThreshold: 0,       // 默认不启用访问频率重置过期时间
		accessWindow:    time.Minute,
		ctx:             ctx,
		cancel:          cancel,
		hashFunc:        defaultHashFunc[K],
	}

	// 应用选项
	for _, option := range options {
		option(cache)
	}

	// 初始化分片
	cache.shards = make([]*cacheShard[K, V], cache.shardCount)
	for i := 0; i < cache.shardCount; i++ {
		cache.shards[i] = &cacheShard[K, V]{
			items:   make(map[K]*list.Element),
			lruList: list.New(),
		}
	}

	// 启动过期检查
	go cache.startJanitor()

	// 如果有同步提供者，启动同步订阅
	if cache.syncProvider != nil {
		cache.setupSync()
	}

	return cache
}

// WithShardCount 设置分片数量
func WithShardCount[K comparable, V any](count int) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		if count > 0 {
			c.shardCount = count
		}
	}
}

// WithTTL 设置默认TTL
func WithTTL[K comparable, V any](ttl time.Duration) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		if ttl > 0 {
			c.ttl = ttl
		}
	}
}

// WithMaxItems 设置最大项目数量
func WithMaxItems[K comparable, V any](max int) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		if max > 0 {
			c.maxItems = max
		}
	}
}

// WithMaxMemory 设置最大内存使用量（字节）
func WithMaxMemory[K comparable, V any](maxBytes int64) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		if maxBytes > 0 {
			c.maxMemory = maxBytes
		}
	}
}

// WithEvictionCallback 设置淘汰回调
func WithEvictionCallback[K comparable, V any](callback EvictCallback[K, V]) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		c.onEvict = callback
	}
}

// WithSyncProvider 设置同步提供者
func WithSyncProvider[K comparable, V any](provider *RedisSyncProvider[K, V]) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		c.syncProvider = provider
	}
}

// WithAccessThreshold 设置访问阈值
func WithAccessThreshold[K comparable, V any](count int, window time.Duration) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		if count > 0 && window > 0 {
			c.accessThreshold = count
			c.accessWindow = window
		}
	}
}

// WithHashFunc 设置自定义哈希函数
func WithHashFunc[K comparable, V any](hashFunc func(K) uint32) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		c.hashFunc = hashFunc
	}
}

// WithFallback 设置数据获取回退函数
func WithFallback[K comparable, V any](fallback FallbackFunc[K, V]) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		c.fallback = fallback
	}
}

// WithBatchFallback 设置批量数据获取回退函数
func WithBatchFallback[K comparable, V any](fallback BatchFallbackFunc[K, V]) Option[K, V] {
	return func(c *ShardedCache[K, V]) {
		c.batchFallback = fallback
	}
}

// defaultHashFunc 默认哈希函数
func defaultHashFunc[K comparable](key K) uint32 {
	keyStr := fmt.Sprintf("%v", key)
	var hash uint32 = 2166136261
	for i := 0; i < len(keyStr); i++ {
		hash ^= uint32(keyStr[i])
		hash *= 16777619
	}
	return hash
}

// getShard 根据键获取对应的分片
func (c *ShardedCache[K, V]) getShard(key K) *cacheShard[K, V] {
	hash := c.hashFunc(key)
	shardIndex := int(hash % uint32(c.shardCount))
	return c.shards[shardIndex]
}

// Set 设置缓存项
func (c *ShardedCache[K, V]) Set(key K, value V, size int64, ttl ...time.Duration) {
	expireDuration := c.ttl
	if len(ttl) > 0 && ttl[0] > 0 {
		expireDuration = ttl[0]
	}

	expireTime := time.Now().Add(expireDuration)
	item := &CacheItem[K, V]{
		Key:             key,
		Value:           value,
		ExpireTime:      expireTime,
		AccessCount:     0,
		AccessCountTime: time.Now(),
		Size:            size,
	}

	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	// 如果键已存在，先移除旧值
	if element, exists := shard.items[key]; exists {
		oldItem := element.Value.(*CacheItem[K, V])
		shard.lruList.Remove(element)
		shard.currentSize -= oldItem.Size
		atomic.AddInt64(&c.currentMemory, -oldItem.Size)
	}

	// 检查容量和内存限制
	if len(shard.items) >= c.maxItems/c.shardCount || atomic.LoadInt64(&c.currentMemory)+size > c.maxMemory {
		c.evictItems(shard)
	}

	// 添加新项到LRU链表头部
	element := shard.lruList.PushFront(item)
	shard.items[key] = element
	shard.currentSize += size
	atomic.AddInt64(&c.currentMemory, size)

	// 如果有同步提供者，发布设置事件
	if c.syncProvider != nil {
		go c.syncProvider.Publish(SyncEvent[K, V]{
			Type:  SyncEventSet,
			Key:   key,
			Value: value,
			TTL:   expireDuration,
		})
	}
}

// Get 获取缓存项
func (c *ShardedCache[K, V]) Get(key K) (V, bool) {
	shard := c.getShard(key)
	shard.mutex.RLock()
	element, exists := shard.items[key]
	if !exists {
		shard.mutex.RUnlock()
		// 如果有回退函数，尝试获取数据
		if c.fallback != nil {
			value, err := c.fallback(key)
			if err == nil {
				// 获取成功，设置到缓存
				c.Set(key, value, 100) // 默认大小，实际应用中应该准确计算
				return value, true
			}
			var zero V
			return zero, false
		}
		var zero V
		return zero, false
	}

	item := element.Value.(*CacheItem[K, V])
	// 检查是否过期
	if time.Now().After(item.ExpireTime) {
		shard.mutex.RUnlock()
		c.Delete(key) // 异步删除过期项
		// 如果有回退函数，尝试获取数据
		if c.fallback != nil {
			value, err := c.fallback(key)
			if err == nil {
				// 获取成功，设置到缓存
				c.Set(key, value, 100) // 默认大小，实际应用中应该准确计算
				return value, true
			}
			var zero V
			return zero, false
		}
		var zero V
		return zero, false
	}
	shard.mutex.RUnlock()

	// 更新LRU位置和访问计数（需要写锁）
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	// 再次检查项目是否还在缓存中
	element, exists = shard.items[key]
	if !exists {
		var zero V
		return zero, false
	}

	item = element.Value.(*CacheItem[K, V])

	// 移到链表前端
	shard.lruList.MoveToFront(element)

	// 更新访问计数
	now := time.Now()
	if now.Sub(item.AccessCountTime) > c.accessWindow {
		item.AccessCount = 1
		item.AccessCountTime = now
	} else {
		item.AccessCount++
		if c.accessThreshold > 0 && item.AccessCount >= c.accessThreshold {
			item.ExpireTime = now.Add(c.ttl)
			item.AccessCount = 0
			item.AccessCountTime = now
		}
	}

	return item.Value, false
}

// GetBatch 批量获取缓存项
func (c *ShardedCache[K, V]) GetBatch(keys []K) (map[K]V, error) {
	result := make(map[K]V, len(keys))
	missingKeys := make([]K, 0, len(keys))

	// 按分片分组键
	shardKeys := make(map[*cacheShard[K, V]][]K)
	for _, key := range keys {
		shard := c.getShard(key)
		shardKeys[shard] = append(shardKeys[shard], key)
	}

	// 并发处理每个分片
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errChan = make(chan error, 1)

	for shard, keys := range shardKeys {
		wg.Add(1)
		go func(s *cacheShard[K, V], ks []K) {
			defer wg.Done()

			s.mutex.RLock()
			// 收集需要更新的项和缺失的键
			var itemsToUpdate []*list.Element
			now := time.Now()

			for _, key := range ks {
				if element, exists := s.items[key]; exists {
					item := element.Value.(*CacheItem[K, V])
					if !now.After(item.ExpireTime) {
						mu.Lock()
						result[key] = item.Value
						mu.Unlock()
						itemsToUpdate = append(itemsToUpdate, element)
					} else {
						// 异步删除过期项
						go c.Delete(key)
						mu.Lock()
						missingKeys = append(missingKeys, key)
						mu.Unlock()
					}
				} else {
					mu.Lock()
					missingKeys = append(missingKeys, key)
					mu.Unlock()
				}
			}
			s.mutex.RUnlock()

			// 如果有需要更新的项，获取写锁进行更新
			if len(itemsToUpdate) > 0 {
				s.mutex.Lock()
				for _, element := range itemsToUpdate {
					item := element.Value.(*CacheItem[K, V])
					s.lruList.MoveToFront(element)

					// 更新访问计数
					if now.Sub(item.AccessCountTime) > c.accessWindow {
						item.AccessCount = 1
						item.AccessCountTime = now
					} else {
						item.AccessCount++
						if c.accessThreshold > 0 && item.AccessCount >= c.accessThreshold {
							item.ExpireTime = now.Add(c.ttl)
							item.AccessCount = 0
							item.AccessCountTime = now
						}
					}
				}
				s.mutex.Unlock()
			}
		}(shard, keys)
	}

	wg.Wait()

	// 如果有缺失的键且存在批量回退函数，尝试获取数据
	if len(missingKeys) > 0 && c.batchFallback != nil {
		values, err := c.batchFallback(missingKeys)
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
			return result, err
		}

		// 将获取到的数据设置到缓存
		for key, value := range values {
			result[key] = value
			c.Set(key, value, 100) // 默认大小，实际应用中应该准确计算
		}
	}

	select {
	case err := <-errChan:
		return result, err
	default:
		return result, nil
	}
}

// Delete 从缓存中删除项目
func (c *ShardedCache[K, V]) Delete(key K) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	element, exists := shard.items[key]
	if !exists {
		return
	}

	item := element.Value.(*CacheItem[K, V])
	delete(shard.items, key)
	shard.lruList.Remove(element)
	shard.currentSize -= item.Size
	atomic.AddInt64(&c.currentMemory, -item.Size)

	if c.onEvict != nil {
		c.onEvict(key, item.Value)
	}

	if c.syncProvider != nil {
		go c.syncProvider.Publish(SyncEvent[K, V]{
			Type: SyncEventDelete,
			Key:  key,
		})
	}
}

// evictItems 根据LRU策略淘汰项目
func (c *ShardedCache[K, V]) evictItems(shard *cacheShard[K, V]) {
	if shard.lruList.Len() == 0 {
		return
	}

	element := shard.lruList.Back()
	item := element.Value.(*CacheItem[K, V])
	key := item.Key

	delete(shard.items, key)
	shard.lruList.Remove(element)
	shard.currentSize -= item.Size
	atomic.AddInt64(&c.currentMemory, -item.Size)

	if c.onEvict != nil {
		c.onEvict(key, item.Value)
	}
}

// Clear 清空缓存
func (c *ShardedCache[K, V]) Clear() {
	for _, shard := range c.shards {
		shard.mutex.Lock()

		if c.onEvict != nil {
			for key, element := range shard.items {
				item := element.Value.(*CacheItem[K, V])
				c.onEvict(key, item.Value)
			}
		}

		shard.items = make(map[K]*list.Element)
		shard.lruList = list.New()
		shard.currentSize = 0

		shard.mutex.Unlock()
	}

	atomic.StoreInt64(&c.currentMemory, 0)
}

// Close 关闭缓存
func (c *ShardedCache[K, V]) Close() {
	c.cancel()
}

// GetSize 获取当前缓存使用的内存大小
func (c *ShardedCache[K, V]) GetSize() int64 {
	return atomic.LoadInt64(&c.currentMemory)
}

// GetItemCount 获取当前缓存中的项目数量
func (c *ShardedCache[K, V]) GetItemCount() int {
	count := 0
	for _, shard := range c.shards {
		shard.mutex.RLock()
		count += len(shard.items)
		shard.mutex.RUnlock()
	}
	return count
}

// startJanitor 启动过期检查
func (c *ShardedCache[K, V]) startJanitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.removeExpired()
		case <-c.ctx.Done():
			return
		}
	}
}

// removeExpired 移除过期项目
func (c *ShardedCache[K, V]) removeExpired() {
	now := time.Now()

	for _, shard := range c.shards {
		var keysToDelete []K

		shard.mutex.RLock()
		for key, element := range shard.items {
			item := element.Value.(*CacheItem[K, V])
			if now.After(item.ExpireTime) {
				keysToDelete = append(keysToDelete, key)
			}
		}
		shard.mutex.RUnlock()

		for _, key := range keysToDelete {
			c.Delete(key)
		}
	}
}

// setupSync 设置分布式同步
func (c *ShardedCache[K, V]) setupSync() {
	if c.syncProvider == nil {
		return
	}

	err := c.syncProvider.Subscribe(func(event SyncEvent[K, V]) {
		switch event.Type {
		case SyncEventSet:
			size := int64(1000) // 默认大小，实际应用中应该准确计算
			c.Set(event.Key, event.Value, size, event.TTL)
		case SyncEventDelete:
			c.Delete(event.Key)
		}
	})

	if err != nil {
		fmt.Printf("Failed to setup cache sync: %v\n", err)
	}
}

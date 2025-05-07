package mutil_level_cache

import (
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"
)

// 性能测试
func BenchmarkCacheSet(b *testing.B) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Set("key", "value", time.Minute)
		}
	})
}

func BenchmarkCacheGet(b *testing.B) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	// 预热缓存
	for i := 0; i < 1000; i++ {
		cache.Set("key"+string(rune(i)), "value", time.Minute)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("key" + string(rune(b.N%1000)))
		}
	})
}

func testCacheConcurrentAccess(policy EvictionPolicy, t *testing.T) {
	now := time.Now()
	defer func() {
		slog.Info("testCacheConcurrentAccess", slog.Any("policy", policy), slog.Any("cost", time.Since(now)))
	}()
	cache := NewCache[string, int](CacheConfig{
		ShardCount:      32,
		MaxItems:        10000,
		EvictionPolicy:  policy,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	var wg sync.WaitGroup
	goroutines := 100
	operations := 100000

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				key := "key" + string(rune(id))
				value := id*1000 + j

				// 并发写入
				cache.Set(key, value, time.Minute)

				// 并发读取
				if v, found := cache.Get(key); found {
					if v != value {
						t.Errorf("并发读取错误: 期望 %d, 得到 %d", value, v)
					}
				}

				// 并发删除
				cache.Delete(key)
			}
		}(i)
	}

	wg.Wait()
}

// 并发安全性测试
func TestCacheConcurrentAccess(t *testing.T) {
	policies := []EvictionPolicy{LRU,
		LFU,
		FIFO,
		Random,
		TTL,
		TinyLFU,
		DoubleLRULFU,
		SWTinyLFU}
	for _, p := range policies {
		testCacheConcurrentAccess(p, t)
	}
}

// 内存泄漏测试
func TestCacheMemoryLeak(t *testing.T) {
	cache := NewCache[string, []byte](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	// 记录初始内存使用
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	initialAlloc := m.TotalAlloc

	// 创建大量数据
	largeData := make([]byte, 1024*1024) // 1MB
	for i := 0; i < 1000; i++ {
		cache.Set("key"+string(rune(i)), largeData, time.Minute)
	}

	// 强制GC
	runtime.GC()

	// 检查内存使用
	runtime.ReadMemStats(&m)
	finalAlloc := m.TotalAlloc

	// 清理缓存
	for i := 0; i < 1000; i++ {
		cache.Delete("key" + string(rune(i)))
	}

	// 再次强制GC
	runtime.GC()

	// 检查内存是否被正确释放
	runtime.ReadMemStats(&m)
	afterCleanupAlloc := m.TotalAlloc

	// 验证内存使用
	if finalAlloc-initialAlloc < 1000*1024*1024 {
		t.Error("内存分配异常: 预期分配更多内存")
	}

	if afterCleanupAlloc-initialAlloc > 100*1024*1024 {
		t.Error("可能存在内存泄漏: 清理后内存未完全释放")
	}
}

// 过期清理测试
func TestCacheExpiration(t *testing.T) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      100 * time.Millisecond,
		CleanupInterval: 50 * time.Millisecond,
	})

	// 设置短期过期的项
	cache.Set("short", "value", 100*time.Millisecond)

	// 设置长期过期的项
	cache.Set("long", "value", time.Hour)

	// 等待短期项过期
	time.Sleep(200 * time.Millisecond)

	// 检查短期项是否已过期
	if _, found := cache.Get("short"); found {
		t.Error("短期项未正确过期")
	}

	// 检查长期项是否仍然存在
	if _, found := cache.Get("long"); !found {
		t.Error("长期项被错误地清理")
	}
}

// 容量限制测试
func TestCacheCapacityLimit(t *testing.T) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        100,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	// 填充缓存
	for i := 0; i < 200; i++ {
		cache.Set("key"+string(rune(i)), "value", time.Minute)
	}

	// 验证缓存大小不超过限制
	for i := 0; i < cache.shardCount; i++ {
		shard := cache.shards[i]
		shard.mutex.RLock()
		if len(shard.items) > shard.capacity {
			t.Errorf("分片 %d 超出容量限制: %d > %d", i, len(shard.items), shard.capacity)
		}
		shard.mutex.RUnlock()
	}
}

// 重置TTL测试
func TestCacheResetTTL(t *testing.T) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	// 设置初始TTL
	initialTTL := 100 * time.Millisecond
	cache.Set("key", "value", initialTTL)

	// 等待一段时间
	time.Sleep(50 * time.Millisecond)

	// 重置TTL
	cache.ResetTTL("key")

	// 再次等待
	time.Sleep(60 * time.Millisecond)

	// 验证项仍然存在
	if _, found := cache.Get("key"); !found {
		t.Error("重置TTL后项被错误地清理")
	}
}

// 批量操作测试
func TestCacheBatchOperations(t *testing.T) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	// 准备测试数据
	keys := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		keys[i] = "key" + string(rune(i))
		cache.Set(keys[i], "value"+string(rune(i)), time.Minute)
	}

	// 测试批量获取
	results := cache.GetMulti(keys)
	if len(results) != 1000 {
		t.Errorf("批量获取结果数量错误: 期望 1000, 得到 %d", len(results))
	}

	// 验证所有值
	for i := 0; i < 1000; i++ {
		key := "key" + string(rune(i))
		if value, found := results[key]; !found || value != "value"+string(rune(i)) {
			t.Errorf("批量获取值错误: key=%s", key)
		}
	}
}

// 并发删除测试
func TestCacheConcurrentDelete(t *testing.T) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        10000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	// 预热缓存
	for i := 0; i < 1000; i++ {
		cache.Set("key"+string(rune(i)), "value", time.Minute)
	}

	var wg sync.WaitGroup
	goroutines := 100
	operations := 1000

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				key := "key" + string(rune((id+j)%1000))

				// 并发删除
				cache.Delete(key)

				// 验证删除是否成功
				if _, found := cache.Get(key); found {
					t.Errorf("删除后项仍然存在: key=%s", key)
				}
			}
		}(i)
	}

	wg.Wait()
}

// 复杂并发测试
func TestCacheComplexConcurrent(t *testing.T) {
	cache := NewCache[string, string](CacheConfig{
		ShardCount:      16,
		MaxItems:        1000,
		EvictionPolicy:  LRU,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: time.Minute,
	})

	var wg sync.WaitGroup
	goroutines := 100
	operations := 1000

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				key := "key" + string(rune((id+j)%1000))
				value := "value" + string(rune(j))

				// 随机执行不同操作
				switch j % 4 {
				case 0:
					// 设置操作
					cache.Set(key, value, time.Minute)
				case 1:
					// 获取操作
					cache.Get(key)
				case 2:
					// 删除操作
					cache.Delete(key)
				case 3:
					// 批量操作
					keys := make([]string, 10)
					for k := 0; k < 10; k++ {
						keys[k] = "key" + string(rune((id+j+k)%1000))
					}
					cache.GetMulti(keys)
				}
			}
		}(i)
	}

	wg.Wait()

	// 验证缓存状态
	shard := cache.shards[0]
	shard.mutex.RLock()
	defer shard.mutex.RUnlock()

	// 验证数据结构一致性
	for key := range shard.items {
		// 验证LRU
		if shard.policy == LRU {
			if _, ok := shard.lruItems[key]; !ok {
				t.Errorf("LRU不一致: key=%s 在items中存在但不在lruItems中", key)
			}
		}

		// 验证FIFO
		if shard.policy == FIFO {
			if _, ok := shard.fifoItems[key]; !ok {
				t.Errorf("FIFO不一致: key=%s 在items中存在但不在fifoItems中", key)
			}
		}

		// 验证内存使用
		if shard.currentSize < 0 {
			t.Errorf("内存使用异常: currentSize=%d", shard.currentSize)
		}
	}
}

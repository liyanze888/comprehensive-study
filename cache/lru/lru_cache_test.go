package lru

import (
	"sync"
	"testing"
	"time"
)

// 并发安全性测试
func TestCacheConcurrentAccess(t *testing.T) {
	// 创建缓存实例
	cache := NewShardedCache[string, int](
		WithShardCount[string, int](32), // 增加分片数以提高并发性能
		WithTTL[string, int](time.Hour),
		WithMaxItems[string, int](10000),
		WithMaxMemory[string, int](1<<30),
	)

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
				cache.Set(key, value, 1, time.Minute)

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

package main

import (
	"comprehensive-study/cache/lru"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// User 测试用的用户结构
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Data []byte `json:"data"` // 添加一些数据以模拟真实场景
}

func main() {
	provider, err := lru.NewRedisSyncProvider[int, User]()
	if err != nil {
		panic(err)
	}
	// 创建缓存实例
	cache := lru.NewShardedCache[int, User](
		lru.WithShardCount[int, User](32), // 增加分片数以提高并发性能
		lru.WithTTL[int, User](time.Hour),
		lru.WithMaxItems[int, User](1000000),
		lru.WithMaxMemory[int, User](1<<30),
		lru.WithSyncProvider(provider),
	)

	// 预热缓存
	fmt.Println("预热缓存...")
	for i := 0; i < 10000; i++ {
		cache.Set(i, User{
			ID:   i,
			Name: fmt.Sprintf("User%d", i),
			Data: make([]byte, 1024), // 1KB数据
		}, 1024)
	}

	// 并发写入测试
	fmt.Println("\n开始并发写入测试...")
	start := time.Now()
	var writeOps uint64
	var wg sync.WaitGroup
	for i := 0; i < 10000; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cache.Set(id+10000, User{
				ID:   id + 10000,
				Name: fmt.Sprintf("User%d", id+10000),
				Data: make([]byte, 1024),
			}, 1024)
			atomic.AddUint64(&writeOps, 1)
		}(i)
	}
	wg.Wait()
	duration := time.Since(start)
	fmt.Printf("并发写入性能: %d ops/sec\n", int(float64(writeOps)/duration.Seconds()))

	// 并发读取测试
	fmt.Println("\n开始并发读取测试...")
	start = time.Now()
	var readOps uint64
	for i := 0; i < 10000; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, _ = cache.Get(id % 20000) // 读取已存在和未存在的键
			atomic.AddUint64(&readOps, 1)
		}(i)
	}
	wg.Wait()
	duration = time.Since(start)
	fmt.Printf("并发读取性能: %d ops/sec\n", int(float64(readOps)/duration.Seconds()))

	// 并发读写混合测试
	fmt.Println("\n开始并发读写混合测试...")
	start = time.Now()
	var mixedOps uint64
	for i := 0; i < 10000; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				cache.Set(id+20000, User{
					ID:   id + 20000,
					Name: fmt.Sprintf("User%d", id+20000),
					Data: make([]byte, 1024),
				}, 1024)
			} else {
				_, _ = cache.Get(id % 30000)
			}
			atomic.AddUint64(&mixedOps, 1)
		}(i)
	}
	wg.Wait()
	duration = time.Since(start)
	fmt.Printf("并发读写混合性能: %d ops/sec\n", int(float64(mixedOps)/duration.Seconds()))

	// 批量操作测试
	fmt.Println("\n开始批量操作测试...")
	start = time.Now()
	var batchOps uint64
	batchSize := 100
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(batchID int) {
			defer wg.Done()
			keys := make([]int, batchSize)
			for j := 0; j < batchSize; j++ {
				keys[j] = batchID*batchSize + j
			}
			_, _ = cache.GetBatch(keys)
			atomic.AddUint64(&batchOps, uint64(batchSize))
		}(i)
	}
	wg.Wait()
	duration = time.Since(start)
	fmt.Printf("批量操作性能: %d ops/sec\n", int(float64(batchOps)/duration.Seconds()))

	// 输出统计信息
	fmt.Printf("\n缓存统计信息:\n")
	fmt.Printf("当前缓存大小: %d bytes\n", cache.GetSize())
	fmt.Printf("当前缓存项数: %d\n", cache.GetItemCount())
}

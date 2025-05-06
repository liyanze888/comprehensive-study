package main

import (
	"comprehensive-study/cache/lru"
	"fmt"
	"log"
	"time"
)

// User 示例用户类型
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	// 创建缓存实例
	cache := lru.NewShardedCache[int, User](
		lru.WithShardCount[int, User](16),
		lru.WithTTL[int, User](time.Hour),
		lru.WithMaxItems[int, User](100000),
		lru.WithMaxMemory[int, User](1<<30),
		lru.WithAccessThreshold[int, User](10, time.Minute),
		lru.WithEvictionCallback[int, User](func(key int, value User) {
			fmt.Printf("Item evicted: %d\n", key)
		}),
		lru.WithFallback[int, User](func(key int) (User, error) {
			// 模拟从数据库获取数据
			return User{ID: key, Name: fmt.Sprintf("User%d", key)}, nil
		}),
		lru.WithBatchFallback[int, User](func(keys []int) (map[int]User, error) {
			// 模拟批量从数据库获取数据
			result := make(map[int]User)
			for _, key := range keys {
				result[key] = User{ID: key, Name: fmt.Sprintf("User%d", key)}
			}
			return result, nil
		}),
	)

	// 设置值
	user := User{ID: 1, Name: "张三"}
	cache.Set(1, user, 100)

	// 获取单个值
	value, err := cache.Get(1)
	if err != nil {
		log.Printf("Error getting value: %v\n", err)
	} else {
		fmt.Printf("Got user: %+v\n", value)
	}

	// 获取不存在的值（会触发fallback）
	value, err = cache.Get(2)
	if err != nil {
		log.Printf("Error getting value: %v\n", err)
	} else {
		fmt.Printf("Got user from fallback: %+v\n", value)
	}

	// 批量获取值
	keys := []int{1, 2, 3, 4, 5}
	values, err := cache.GetBatch(keys)
	if err != nil {
		log.Printf("Error getting batch values: %v\n", err)
	} else {
		fmt.Printf("Got batch users: %+v\n", values)
	}

	// 等待一段时间，让过期检查运行
	time.Sleep(time.Minute)

	// 获取缓存统计信息
	fmt.Printf("Cache size: %d bytes\n", cache.GetSize())
	fmt.Printf("Cache item count: %d\n", cache.GetItemCount())

	// 关闭缓存
	cache.Close()
}

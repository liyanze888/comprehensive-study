package main

import (
	"context"
	"fmt"
	"time"

	cache "comprehensive-study/cache/yz" // 导入我们的缓存包
)

// User 示例数据类型
type User struct {
	ID       int
	Name     string
	Email    string
	LastSeen time.Time
}

// 自定义大小计算函数
func calculateUserSize(key int, value User) int64 {
	// 粗略估计User结构体的内存占用
	return int64(8 + len(value.Name) + len(value.Email) + 8)
}

// 演示缓存的淘汰回调函数
func evictionHandler(key int, value User, reason cache.EvictionReason) {
	fmt.Printf("用户 %d (%s) 被淘汰，原因: %s\n", key, value.Name, reason)
}

// 远程缓存回源函数示例
func remoteCache(ctx context.Context, key int) (User, error) {
	fmt.Printf("从远程缓存获取键 %d\n", key)
	// 模拟从远程缓存获取
	if key == 42 {
		return User{ID: key, Name: "远程用户", Email: "remote@example.com"}, nil
	}
	return User{}, fmt.Errorf("远程缓存未找到")
}

// 数据库回源函数示例
func dbFallback(ctx context.Context, key int) (User, error) {
	fmt.Printf("从数据库获取键 %d\n", key)
	// 模拟从数据库获取
	return User{
		ID:       key,
		Name:     fmt.Sprintf("用户-%d", key),
		Email:    fmt.Sprintf("user%d@example.com", key),
		LastSeen: time.Now(),
	}, nil
}

func main() {
	// 1. 创建缓存配置
	cfg := cache.Config[int, User]{
		ShardCount:      32,                                                       // 32个分片
		MaxItems:        1000,                                                     // 最多存储1000项
		MaxMemoryBytes:  10 * 1024 * 1024,                                         // 最大10MB内存
		DefaultTTL:      time.Minute * 5,                                          // 默认5分钟过期
		ResetTTLOnHits:  3,                                                        // 在3次访问后
		ResetTTLWindow:  time.Second * 30,                                         // 在30秒内
		EvictionPolicy:  cache.LRU,                                                // 使用LRU淘汰策略
		EvictionHandler: evictionHandler,                                          // 设置淘汰回调
		SizeFunc:        calculateUserSize,                                        // 自定义大小计算
		Fallbacks:       []cache.FallbackFunc[int, User]{remoteCache, dbFallback}, // 多级回源
	}

	// 2. 创建缓存实例
	userCache := cache.NewCache(cfg)
	defer userCache.Stop() // 确保退出前停止后台任务

	// 3. 添加一些数据
	ctx := context.Background()

	err := userCache.Set(1, User{ID: 1, Name: "张三", Email: "zhang@example.com"})
	if err != nil {
		fmt.Printf("设置缓存错误: %v\n", err)
	}

	err = userCache.Set(2, User{ID: 2, Name: "李四", Email: "li@example.com"}, time.Second*30) // 自定义TTL
	if err != nil {
		fmt.Printf("设置缓存错误: %v\n", err)
	}

	// 4. 获取数据
	if user, found := userCache.Get(ctx, 1); found {
		fmt.Printf("找到用户: %s (%s)\n", user.Name, user.Email)
	} else {
		fmt.Println("未找到用户1")
	}

	// 5. 使用多级回源获取数据
	user, found := userCache.Get(ctx, 42) // 这会触发回源
	if found {
		fmt.Printf("通过回源找到用户42: %s\n", user.Name)
	}

	// 6. 直接使用单个回源函数
	user, err = userCache.GetWithFallback(ctx, 100, dbFallback)
	if err == nil {
		fmt.Printf("通过单独回源找到用户100: %s\n", user.Name)
	}

	// 7. 批量获取
	users := userCache.GetMulti(ctx, []int{1, 3, 5})
	fmt.Printf("批量获取到 %d 个用户\n", len(users))

	// 8. 显示统计信息
	metrics := userCache.GetMetrics()
	fmt.Printf("\n缓存统计:\n")
	fmt.Printf("- 命中次数: %d\n", metrics.Hits)
	fmt.Printf("- 未命中次数: %d\n", metrics.Misses)
	fmt.Printf("- 淘汰总数: %d\n", metrics.Evictions)
	fmt.Printf("- 过期淘汰: %d\n", metrics.ExpiredEvictions)
	fmt.Printf("- 内存淘汰: %d\n", metrics.MemoryEvictions)
	fmt.Printf("- 容量淘汰: %d\n", metrics.CapacityEvictions)

	fmt.Printf("\n缓存大小: %d 项\n", userCache.Size())
	fmt.Printf("内存使用: %.2f MB\n", float64(userCache.MemoryUsage())/(1024*1024))

	// 9. 演示删除
	if userCache.Delete(1) {
		fmt.Println("用户1已删除")
	}

	// 10. 清空缓存
	userCache.Clear()
	fmt.Printf("缓存已清空，当前大小: %d\n", userCache.Size())
}

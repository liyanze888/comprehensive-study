package mutil_level_cache

import "time"

// 缓存配置
type CacheConfig struct {
	ShardCount      int            // 分片数量
	EvictionPolicy  EvictionPolicy // 淘汰策略
	DefaultTTL      time.Duration  // 默认TTL
	MaxMemory       int64          // 内存上限(字节)
	MaxItems        int            // 最大项数
	CleanupInterval time.Duration  // 清理间隔
}

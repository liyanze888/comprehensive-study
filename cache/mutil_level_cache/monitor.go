package mutil_level_cache

import (
	"sync/atomic"
)

// CacheMetrics  缓存指标
type CacheMonitorMetrics struct {
	hits          atomic.Int64 // 命中次数
	misses        atomic.Int64 // 未命中次数
	evictions     atomic.Int64 // 淘汰次数
	memoryUsage   atomic.Int64 // 内存使用量
	remoteHits    atomic.Int64 // 远程缓存命中
	remoteMisses  atomic.Int64 // 远程缓存未命中
	fallbackCalls atomic.Int64 // 回调函数调用次数
}

// 缓存指标快照
type CacheMonitorMetricsSnapshot struct {
	Hits          int64
	Misses        int64
	Evictions     int64
	MemoryUsage   int64
	RemoteHits    int64
	RemoteMisses  int64
	FallbackCalls int64
	ItemCount     int
}

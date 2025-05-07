package mutil_level_cache

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

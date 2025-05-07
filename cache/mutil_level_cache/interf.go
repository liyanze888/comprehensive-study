package mutil_level_cache

import "time"

// CacheListener 通知接口
type CacheListener[K comparable, V any] interface {
	OnEviction(key K, value V, reason EvictionReason)
}

// RemoteCache 远程缓存接口 用来做一级缓存，进行redis 或其他存储放方式的
type RemoteCache[K comparable, V any] interface {
	Get(key K) (V, bool)
	Set(key K, value V, ttl time.Duration)
	Delete(key K)
	GetMulti(keys []K) map[K]V
	SetMulti(items map[K]V, ttl time.Duration)
}

// DistributedPeer 分布式同步接口 用来刷新多节点的数据
type DistributedPeer[K comparable, V any] interface {
	SyncSet(key K, value V, ttl time.Duration)
	SyncDelete(key K)
	SyncSetMulti(items map[K]V, ttl time.Duration)
}

type FallbackLoader[K comparable, V any] interface {
	Load(key K) (V, error)
	BatchLoad(keys []K) (map[K]V, error)
}

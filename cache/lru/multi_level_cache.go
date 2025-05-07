package lru

import (
	"context"
	"fmt"
	"time"
)

// CacheLevel 表示缓存级别
type CacheLevel int

const (
	LevelL1 CacheLevel = iota // 本地内存缓存
	LevelL2                   // 分布式缓存（如Redis）
	LevelL3                   // 持久化存储（如数据库）
)

// CacheProvider 缓存提供者接口
type CacheProvider[K comparable, V any] interface {
	// Get 从缓存中获取值
	Get(ctx context.Context, key K) (V, error)
	// Set 设置缓存值
	Set(ctx context.Context, key K, value V, ttl time.Duration) error
	// Delete 删除缓存值
	Delete(ctx context.Context, key K) error
	// GetBatch 批量获取缓存值
	GetBatch(ctx context.Context, keys []K) (map[K]V, error)
	// SetBatch 批量设置缓存值
	SetBatch(ctx context.Context, items map[K]V, ttl time.Duration) error
	// Level 返回缓存级别
	Level() CacheLevel
}

// MultiLevelCache 多级缓存结构
type MultiLevelCache[K comparable, V any] struct {
	providers []CacheProvider[K, V]
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewMultiLevelCache 创建新的多级缓存
func NewMultiLevelCache[K comparable, V any](providers ...CacheProvider[K, V]) *MultiLevelCache[K, V] {
	ctx, cancel := context.WithCancel(context.Background())
	return &MultiLevelCache[K, V]{
		providers: providers,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Get 从多级缓存中获取值
func (c *MultiLevelCache[K, V]) Get(ctx context.Context, key K) (V, error) {
	var zero V
	var lastErr error

	// 按级别顺序尝试获取
	for _, provider := range c.providers {
		value, err := provider.Get(ctx, key)
		if err == nil {
			// 获取成功，将值写入更高级别的缓存
			c.writeToHigherLevels(ctx, key, value, provider.Level())
			return value, nil
		}
		lastErr = err
	}

	return zero, fmt.Errorf("failed to get value from all cache levels: %v", lastErr)
}

// Set 设置多级缓存的值
func (c *MultiLevelCache[K, V]) Set(ctx context.Context, key K, value V, ttl time.Duration) error {
	var lastErr error

	// 写入所有级别的缓存
	for _, provider := range c.providers {
		if err := provider.Set(ctx, key, value, ttl); err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("failed to set value in some cache levels: %v", lastErr)
	}
	return nil
}

// Delete 从多级缓存中删除值
func (c *MultiLevelCache[K, V]) Delete(ctx context.Context, key K) error {
	var lastErr error

	// 从所有级别的缓存中删除
	for _, provider := range c.providers {
		if err := provider.Delete(ctx, key); err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("failed to delete value from some cache levels: %v", lastErr)
	}
	return nil
}

// GetBatch 批量获取多级缓存的值
func (c *MultiLevelCache[K, V]) GetBatch(ctx context.Context, keys []K) (map[K]V, error) {
	result := make(map[K]V)
	remainingKeys := make([]K, len(keys))
	copy(remainingKeys, keys)

	// 按级别顺序尝试批量获取
	for _, provider := range c.providers {
		if len(remainingKeys) == 0 {
			break
		}

		values, err := provider.GetBatch(ctx, remainingKeys)
		if err == nil {
			// 更新结果
			for k, v := range values {
				result[k] = v
			}

			// 将获取到的值写入更高级别的缓存
			c.writeBatchToHigherLevels(ctx, values, provider.Level())

			// 更新剩余需要查询的键
			newRemainingKeys := make([]K, 0, len(remainingKeys))
			for _, k := range remainingKeys {
				if _, exists := values[k]; !exists {
					newRemainingKeys = append(newRemainingKeys, k)
				}
			}
			remainingKeys = newRemainingKeys
		}
	}

	if len(remainingKeys) > 0 {
		return result, fmt.Errorf("failed to get values for some keys: %v", remainingKeys)
	}
	return result, nil
}

// SetBatch 批量设置多级缓存的值
func (c *MultiLevelCache[K, V]) SetBatch(ctx context.Context, items map[K]V, ttl time.Duration) error {
	var lastErr error

	// 写入所有级别的缓存
	for _, provider := range c.providers {
		if err := provider.SetBatch(ctx, items, ttl); err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("failed to set values in some cache levels: %v", lastErr)
	}
	return nil
}

// writeToHigherLevels 将值写入更高级别的缓存
func (c *MultiLevelCache[K, V]) writeToHigherLevels(ctx context.Context, key K, value V, currentLevel CacheLevel) {
	for _, provider := range c.providers {
		if provider.Level() < currentLevel {
			_ = provider.Set(ctx, key, value, time.Hour) // 使用默认TTL
		}
	}
}

// writeBatchToHigherLevels 批量将值写入更高级别的缓存
func (c *MultiLevelCache[K, V]) writeBatchToHigherLevels(ctx context.Context, items map[K]V, currentLevel CacheLevel) {
	for _, provider := range c.providers {
		if provider.Level() < currentLevel {
			_ = provider.SetBatch(ctx, items, time.Hour) // 使用默认TTL
		}
	}
}

// Close 关闭多级缓存
func (c *MultiLevelCache[K, V]) Close() {
	c.cancel()
}

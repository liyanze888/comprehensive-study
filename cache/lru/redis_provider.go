package lru

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisProvider Redis缓存提供者
type RedisProvider[K comparable, V any] struct {
	client  *redis.Client
	level   CacheLevel
	encoder func(V) ([]byte, error)
	decoder func([]byte) (V, error)
}

// NewRedisProvider 创建新的Redis缓存提供者
func NewRedisProvider[K comparable, V any](
	client *redis.Client,
	encoder func(V) ([]byte, error),
	decoder func([]byte) (V, error),
) *RedisProvider[K, V] {
	return &RedisProvider[K, V]{
		client:  client,
		level:   LevelL2,
		encoder: encoder,
		decoder: decoder,
	}
}

// Level 返回缓存级别
func (p *RedisProvider[K, V]) Level() CacheLevel {
	return p.level
}

// Get 从Redis获取值
func (p *RedisProvider[K, V]) Get(ctx context.Context, key K) (V, error) {
	var zero V
	keyStr := fmt.Sprintf("%v", key)
	data, err := p.client.Get(ctx, keyStr).Bytes()
	if err != nil {
		return zero, err
	}

	value, err := p.decoder(data)
	if err != nil {
		return zero, fmt.Errorf("failed to decode value: %v", err)
	}

	return value, nil
}

// Set 设置Redis值
func (p *RedisProvider[K, V]) Set(ctx context.Context, key K, value V, ttl time.Duration) error {
	data, err := p.encoder(value)
	if err != nil {
		return fmt.Errorf("failed to encode value: %v", err)
	}

	keyStr := fmt.Sprintf("%v", key)
	return p.client.Set(ctx, keyStr, data, ttl).Err()
}

// Delete 删除Redis值
func (p *RedisProvider[K, V]) Delete(ctx context.Context, key K) error {
	keyStr := fmt.Sprintf("%v", key)
	return p.client.Del(ctx, keyStr).Err()
}

// GetBatch 批量获取Redis值
func (p *RedisProvider[K, V]) GetBatch(ctx context.Context, keys []K) (map[K]V, error) {
	if len(keys) == 0 {
		return make(map[K]V), nil
	}

	// 转换键为字符串
	keyStrs := make([]string, len(keys))
	keyMap := make(map[string]K)
	for i, key := range keys {
		keyStr := fmt.Sprintf("%v", key)
		keyStrs[i] = keyStr
		keyMap[keyStr] = key
	}

	// 批量获取
	values, err := p.client.MGet(ctx, keyStrs...).Result()
	if err != nil {
		return nil, err
	}

	// 处理结果
	result := make(map[K]V)
	for i, val := range values {
		if val == nil {
			continue
		}

		data, ok := val.(string)
		if !ok {
			continue
		}

		value, err := p.decoder([]byte(data))
		if err != nil {
			continue
		}

		result[keyMap[keyStrs[i]]] = value
	}

	return result, nil
}

// SetBatch 批量设置Redis值
func (p *RedisProvider[K, V]) SetBatch(ctx context.Context, items map[K]V, ttl time.Duration) error {
	if len(items) == 0 {
		return nil
	}

	pipe := p.client.Pipeline()
	for key, value := range items {
		data, err := p.encoder(value)
		if err != nil {
			return fmt.Errorf("failed to encode value for key %v: %v", key, err)
		}

		keyStr := fmt.Sprintf("%v", key)
		pipe.Set(ctx, keyStr, data, ttl)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// DefaultJSONEncoder 默认的JSON编码器
func DefaultJSONEncoder[V any](value V) ([]byte, error) {
	return json.Marshal(value)
}

// DefaultJSONDecoder 默认的JSON解码器
func DefaultJSONDecoder[V any](data []byte) (V, error) {
	var value V
	err := json.Unmarshal(data, &value)
	return value, err
}

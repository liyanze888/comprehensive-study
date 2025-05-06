package lru

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisSyncProvider Redis同步提供者实现
type RedisSyncProvider[K comparable, V any] struct {
	client   *redis.Client
	channel  string
	ctx      context.Context
	cancel   context.CancelFunc
	handlers []func(SyncEvent[K, V])
	stopChan chan struct{}
}

// RedisSyncProviderOption Redis同步提供者配置选项
type RedisSyncProviderOption func(*RedisSyncProviderConfig)

// RedisSyncProviderConfig Redis同步提供者配置
type RedisSyncProviderConfig struct {
	Addr         string
	Password     string
	DB           int
	Channel      string
	PoolSize     int
	MinIdleConns int
}

// NewRedisSyncProvider 创建新的Redis同步提供者
func NewRedisSyncProvider[K comparable, V any](options ...RedisSyncProviderOption) (*RedisSyncProvider[K, V], error) {
	config := &RedisSyncProviderConfig{
		Addr:         "localhost:6379",
		Password:     "",
		DB:           0,
		Channel:      "cache_sync",
		PoolSize:     10,
		MinIdleConns: 5,
	}

	// 应用配置选项
	for _, option := range options {
		option(config)
	}

	// 创建Redis客户端
	client := redis.NewClient(&redis.Options{
		Addr:         config.Addr,
		Password:     config.Password,
		DB:           config.DB,
		PoolSize:     config.PoolSize,
		MinIdleConns: config.MinIdleConns,
	})

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}

	provider := &RedisSyncProvider[K, V]{
		client:   client,
		channel:  config.Channel,
		stopChan: make(chan struct{}),
	}
	provider.ctx, provider.cancel = context.WithCancel(context.Background())

	return provider, nil
}

// WithRedisAddr 设置Redis地址
func WithRedisAddr(addr string) RedisSyncProviderOption {
	return func(c *RedisSyncProviderConfig) {
		c.Addr = addr
	}
}

// WithRedisPassword 设置Redis密码
func WithRedisPassword(password string) RedisSyncProviderOption {
	return func(c *RedisSyncProviderConfig) {
		c.Password = password
	}
}

// WithRedisDB 设置Redis数据库
func WithRedisDB(db int) RedisSyncProviderOption {
	return func(c *RedisSyncProviderConfig) {
		c.DB = db
	}
}

// WithRedisChannel 设置Redis频道
func WithRedisChannel(channel string) RedisSyncProviderOption {
	return func(c *RedisSyncProviderConfig) {
		c.Channel = channel
	}
}

// WithRedisPoolSize 设置Redis连接池大小
func WithRedisPoolSize(size int) RedisSyncProviderOption {
	return func(c *RedisSyncProviderConfig) {
		c.PoolSize = size
	}
}

// WithRedisMinIdleConns 设置Redis最小空闲连接数
func WithRedisMinIdleConns(n int) RedisSyncProviderOption {
	return func(c *RedisSyncProviderConfig) {
		c.MinIdleConns = n
	}
}

// Publish 发布缓存事件到Redis
func (p *RedisSyncProvider[K, V]) Publish(event SyncEvent[K, V]) error {
	// 将事件序列化为JSON
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %v", err)
	}

	// 发布到Redis
	err = p.client.Publish(p.ctx, p.channel, data).Err()
	if err != nil {
		return fmt.Errorf("failed to publish event: %v", err)
	}

	return nil
}

// Subscribe 订阅Redis缓存事件
func (p *RedisSyncProvider[K, V]) Subscribe(handler func(SyncEvent[K, V])) error {
	p.handlers = append(p.handlers, handler)

	// 启动订阅协程
	go p.subscribe()

	return nil
}

// subscribe 处理Redis订阅
func (p *RedisSyncProvider[K, V]) subscribe() {
	pubsub := p.client.Subscribe(p.ctx, p.channel)
	defer pubsub.Close()

	// 处理订阅消息
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.stopChan:
			return
		case msg := <-pubsub.Channel():
			// 解析事件
			var event SyncEvent[K, V]
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				fmt.Printf("Failed to unmarshal event: %v\n", err)
				continue
			}

			// 调用所有处理器
			for _, handler := range p.handlers {
				handler(event)
			}
		}
	}
}

// Close 关闭Redis同步提供者
func (p *RedisSyncProvider[K, V]) Close() error {
	p.cancel()
	close(p.stopChan)
	return p.client.Close()
}

// 使用示例：
/*
func Example() {
    // 创建Redis同步提供者
    provider, err := NewRedisSyncProvider[int, User](
        WithRedisAddr("localhost:6379"),
        WithRedisPassword("password"),
        WithRedisChannel("cache_sync"),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer provider.Close()

    // 创建缓存实例
    cache := NewShardedCache[int, User](
        WithShardCount[int, User](16),
        WithTTL[int, User](time.Hour),
        WithSyncProvider[int, User](provider),
    )

    // 使用缓存
    user := User{ID: 1, Name: "张三"}
    cache.Set(1, user, 100)
}
*/

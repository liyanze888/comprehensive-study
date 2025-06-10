package cache

import (
	"comprehensive-study/recommend/config"
	"comprehensive-study/recommend/models"
	"context"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"time"
)

type FeatureStore struct {
	client *redis.Client
	config *config.CacheConfig
}

func NewFeatureStore(cfg *config.RedisConfig, cacheConfig *config.CacheConfig) *FeatureStore {
	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	return &FeatureStore{
		client: rdb,
		config: cacheConfig,
	}
}

// Ping 测试连接
func (fs *FeatureStore) Ping(ctx context.Context) error {
	return fs.client.Ping(ctx).Err()
}

// Close 关闭连接
func (fs *FeatureStore) Close() error {
	return fs.client.Close()
}

// GetUserProfile 获取用户画像
func (fs *FeatureStore) GetUserProfile(ctx context.Context, userID string) (*models.UserProfile, error) {
	key := fmt.Sprintf("user:profile:%s", userID)

	data, err := fs.client.Get(ctx, key).Result()
	if err == redis.Nil {
		slog.Info("User profile not found for user: %s", userID)
		return nil, fmt.Errorf("user profile not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	var profile models.UserProfile
	if err := json.Unmarshal([]byte(data), &profile); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user profile: %w", err)
	}

	slog.Info("Retrieved user profile for user: %s", userID)
	return &profile, nil
}

// SetUserProfile 设置用户画像
func (fs *FeatureStore) SetUserProfile(ctx context.Context, profile *models.UserProfile) error {
	key := fmt.Sprintf("user:profile:%s", profile.UserID)

	profile.LastUpdated = time.Now()
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("failed to marshal user profile: %w", err)
	}

	if err := fs.client.Set(ctx, key, data, fs.config.UserProfileTTL).Err(); err != nil {
		return fmt.Errorf("failed to set user profile: %w", err)
	}

	slog.Info("Updated user profile for user: %s", profile.UserID)
	return nil
}

// GetRealTimeFeatures 获取实时特征
func (fs *FeatureStore) GetRealTimeFeatures(ctx context.Context, userID string) (*models.RealTimeFeatures, error) {
	key := fmt.Sprintf("user:realtime:%s", userID)

	data, err := fs.client.Get(ctx, key).Result()
	if err == redis.Nil {
		slog.Info("Real-time features not found for user: %s", userID)
		return nil, fmt.Errorf("real-time features not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get real-time features: %w", err)
	}

	var features models.RealTimeFeatures
	if err := json.Unmarshal([]byte(data), &features); err != nil {
		return nil, fmt.Errorf("failed to unmarshal real-time features: %w", err)
	}

	return &features, nil
}

// UpdateRealTimeFeatures 更新实时特征
func (fs *FeatureStore) UpdateRealTimeFeatures(
	ctx context.Context,
	userID string,
	features *models.RealTimeFeatures,
) error {
	key := fmt.Sprintf("user:realtime:%s", userID)

	features.Timestamp = time.Now()
	data, err := json.Marshal(features)
	if err != nil {
		return fmt.Errorf("failed to marshal real-time features: %w", err)
	}

	// 使用较短的TTL，保持实时性
	ttl := time.Hour * 2
	if err := fs.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("failed to update real-time features: %w", err)
	}

	slog.Info("Updated real-time features for user: %s", userID)
	return nil
}

// CacheRecommendations 缓存推荐结果
func (fs *FeatureStore) CacheRecommendations(
	ctx context.Context,
	userID string,
	recommendations []*models.ContentItem,
) error {
	key := fmt.Sprintf("recommend:cache:%s", userID)

	data, err := json.Marshal(recommendations)
	if err != nil {
		return fmt.Errorf("failed to marshal recommendations: %w", err)
	}

	if err := fs.client.Set(ctx, key, data, fs.config.RecommendationTTL).Err(); err != nil {
		return fmt.Errorf("failed to cache recommendations: %w", err)
	}

	slog.Info("Cached %d recommendations for user: %s", len(recommendations), userID)
	return nil
}

// GetCachedRecommendations 获取缓存的推荐结果
func (fs *FeatureStore) GetCachedRecommendations(
	ctx context.Context,
	userID string,
) ([]*models.ContentItem, error) {
	key := fmt.Sprintf("recommend:cache:%s", userID)

	data, err := fs.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("cached recommendations not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get cached recommendations: %w", err)
	}

	var recommendations []*models.ContentItem
	if err := json.Unmarshal([]byte(data), &recommendations); err != nil {
		return nil, fmt.Errorf("failed to unmarshal recommendations: %w", err)
	}

	slog.Info("Retrieved %d cached recommendations for user: %s", len(recommendations), userID)
	return recommendations, nil
}

// RecordUserAction 记录用户行为
func (fs *FeatureStore) RecordUserAction(
	ctx context.Context,
	userID string,
	action string,
	contentID string,
	context_data map[string]interface{},
) error {
	key := fmt.Sprintf("user:action:%s", userID)

	actionData := map[string]interface{}{
		"action":     action,
		"content_id": contentID,
		"timestamp":  time.Now().Unix(),
		"context":    context_data,
	}

	data, err := json.Marshal(actionData)
	if err != nil {
		return fmt.Errorf("failed to marshal action data: %w", err)
	}

	// 使用列表存储用户行为历史
	pipe := fs.client.Pipeline()
	pipe.LPush(ctx, key, data)
	pipe.LTrim(ctx, key, 0, 999)           // 保留最近1000个行为
	pipe.Expire(ctx, key, time.Hour*24*30) // 30天过期

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to record user action: %w", err)
	}

	slog.Info("Recorded action %s for user %s on content %s", action, userID, contentID)
	return nil
}

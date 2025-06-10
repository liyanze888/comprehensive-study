package dedup

import (
	"comprehensive-study/recommend/config"
	"comprehensive-study/recommend/models"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
)

type Deduplicator struct {
	redis  *redis.Client
	config *config.DedupeConfig
}

func NewDeduplicator(redis *redis.Client, config *config.DedupeConfig) *Deduplicator {
	return &Deduplicator{
		redis:  redis,
		config: config,
	}
}

// DeduplicateRecommendations 主要去重方法
func (d *Deduplicator) DeduplicateRecommendations(
	ctx context.Context,
	userID string,
	candidates []*models.ContentItem,
	strategy models.DedupeStrategy,
) ([]*models.ContentItem, error) {

	slog.Info("Starting deduplication for user %s with %d candidates", userID, len(candidates))

	// 获取用户历史推荐记录
	history, err := d.getUserHistory(ctx, userID)
	if err != nil {
		slog.Error("Failed to get user history: %v", err)
		history = []*models.ContentFingerprint{} // 使用空历史继续
	}

	var deduplicated []*models.ContentItem
	seenItems := make(map[string]bool)

	for _, item := range candidates {
		if d.shouldIncludeItem(item, history, strategy, seenItems) {
			deduplicated = append(deduplicated, item)
			seenItems[item.ID] = true
		}
	}

	// 更新用户历史
	if err := d.updateUserHistory(ctx, userID, deduplicated); err != nil {
		slog.Error("Failed to update user history: %v", err)
	}

	slog.Info("Deduplication complete: %d -> %d items", len(candidates), len(deduplicated))
	return deduplicated, nil
}

// shouldIncludeItem 判断是否应该包含某个内容
func (d *Deduplicator) shouldIncludeItem(
	item *models.ContentItem,
	history []*models.ContentFingerprint,
	strategy models.DedupeStrategy,
	seenItems map[string]bool,
) bool {

	// 基本ID去重
	if seenItems[item.ID] {
		return false
	}

	switch strategy {
	case models.DedupeByID:
		return d.dedupeByID(item, history)
	case models.DedupeByContent:
		return d.dedupeByContent(item, history)
	case models.DedupeByFeatures:
		return d.dedupeByFeatures(item, history)
	case models.DedupeHybrid:
		return d.dedupeHybrid(item, history)
	default:
		return true
	}
}

// dedupeByID ID级别去重
func (d *Deduplicator) dedupeByID(item *models.ContentItem, history []*models.ContentFingerprint) bool {
	for _, h := range history {
		if h.ID == item.ID {
			// 检查时间窗口
			if time.Since(h.LastSeen) < d.config.WindowSize {
				return false
			}
		}
	}
	return true
}

// dedupeByContent 内容相似度去重
func (d *Deduplicator) dedupeByContent(item *models.ContentItem, history []*models.ContentFingerprint) bool {
	itemHash := d.calculateContentHash(item)

	for _, h := range history {
		if h.ContentHash == itemHash {
			return false
		}

		// 计算内容相似度（简化实现）
		similarity := d.calculateTextSimilarity(item, h)
		if similarity > d.config.SimilarityThreshold {
			return false
		}
	}
	return true
}

// dedupeByFeatures 特征向量去重
func (d *Deduplicator) dedupeByFeatures(item *models.ContentItem, history []*models.ContentFingerprint) bool {
	for _, h := range history {
		// 通过特征向量计算相似度
		similarity := d.calculateFeatureSimilarity(item, h)
		if similarity > d.config.SimilarityThreshold {
			return false
		}
	}
	return true
}

// dedupeHybrid 混合去重策略
func (d *Deduplicator) dedupeHybrid(item *models.ContentItem, history []*models.ContentFingerprint) bool {
	// 组合多种去重策略
	if !d.dedupeByID(item, history) {
		return false
	}

	if !d.dedupeByContent(item, history) {
		return false
	}

	return d.dedupeByFeatures(item, history)
}

// calculateContentHash 计算内容哈希
func (d *Deduplicator) calculateContentHash(item *models.ContentItem) string {
	content := fmt.Sprintf("%s|%s|%s",
		item.Title, item.Description, strings.Join(item.Tags, ","))

	hash := md5.Sum([]byte(content))
	return fmt.Sprintf("%x", hash)
}

// calculateFeatureHash 计算特征哈希
func (d *Deduplicator) calculateFeatureHash(features map[string]float64) string {
	// 对特征进行排序以确保一致性
	keys := make([]string, 0, len(features))
	for k := range features {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, k := range keys {
		builder.WriteString(fmt.Sprintf("%s:%.4f|", k, features[k]))
	}

	hash := md5.Sum([]byte(builder.String()))
	return fmt.Sprintf("%x", hash)
}

// calculateTextSimilarity 计算文本相似度（简化实现）
func (d *Deduplicator) calculateTextSimilarity(item *models.ContentItem, fingerprint *models.ContentFingerprint) float64 {
	// 这里是简化实现，实际应该基于内容进行比较
	// 可以使用编辑距离、词袋模型等
	return 0.0 // 简化返回0，表示不相似
}

// calculateFeatureSimilarity 计算特征相似度
func (d *Deduplicator) calculateFeatureSimilarity(item *models.ContentItem, fingerprint *models.ContentFingerprint) float64 {
	// 简化实现：基于特征哈希
	itemFeatureHash := d.calculateFeatureHash(item.Features)
	if itemFeatureHash == fingerprint.FeatureHash {
		return 1.0
	}
	return 0.0
}

// cosineSimilarity 余弦相似度计算
func (d *Deduplicator) cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// getUserHistory 获取用户历史记录
func (d *Deduplicator) getUserHistory(
	ctx context.Context,
	userID string,
) ([]*models.ContentFingerprint, error) {
	key := fmt.Sprintf("dedup:history:%s", userID)

	data, err := d.redis.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	var history []*models.ContentFingerprint
	for _, item := range data {
		var fp models.ContentFingerprint
		if err := json.Unmarshal([]byte(item), &fp); err == nil {
			// 过滤过期项目
			if time.Since(fp.LastSeen) < d.config.WindowSize {
				history = append(history, &fp)
			}
		}
	}

	return history, nil
}

// updateUserHistory 更新用户历史记录
func (d *Deduplicator) updateUserHistory(
	ctx context.Context,
	userID string,
	items []*models.ContentItem,
) error {
	if len(items) == 0 {
		return nil
	}

	key := fmt.Sprintf("dedup:history:%s", userID)

	pipe := d.redis.Pipeline()

	for _, item := range items {
		fingerprint := &models.ContentFingerprint{
			ID:          item.ID,
			ContentHash: d.calculateContentHash(item),
			FeatureHash: d.calculateFeatureHash(item.Features),
			LastSeen:    time.Now(),
		}

		data, err := json.Marshal(fingerprint)
		if err != nil {
			continue
		}

		pipe.LPush(ctx, key, data)
	}

	// 限制列表大小
	pipe.LTrim(ctx, key, 0, int64(d.config.MaxCacheSize-1))
	pipe.Expire(ctx, key, d.config.ContentTTL)

	_, err := pipe.Exec(ctx)
	return err
}

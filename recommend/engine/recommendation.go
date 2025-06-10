package engine

import (
	"comprehensive-study/recommend/cache"
	"comprehensive-study/recommend/config"
	"comprehensive-study/recommend/dedup"
	"comprehensive-study/recommend/models"
	"comprehensive-study/recommend/search"
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"
)

type RecommendationEngine struct {
	searchEngine *search.MockTypesenseClient
	featureStore *cache.FeatureStore
	deduplicator *dedup.Deduplicator
	config       *config.Config
}

func NewRecommendationEngine(
	searchEngine *search.MockTypesenseClient,
	featureStore *cache.FeatureStore,
	deduplicator *dedup.Deduplicator,
	config *config.Config,
) *RecommendationEngine {
	return &RecommendationEngine{
		searchEngine: searchEngine,
		featureStore: featureStore,
		deduplicator: deduplicator,
		config:       config,
	}
}

// GetRecommendations 主推荐方法
func (re *RecommendationEngine) GetRecommendations(
	ctx context.Context,
	req *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {

	requestID := re.generateRequestID()
	startTime := time.Now()

	slog.Info("Processing recommendation request %s for user %s", requestID, req.UserID)

	// 参数验证和默认值设置
	if req.Limit <= 0 || req.Limit > re.config.Recommend.MaxLimit {
		req.Limit = re.config.Recommend.DefaultLimit
	}

	// 检查缓存（如果不是实时请求）
	if !req.RealTime {
		if cached, err := re.featureStore.GetCachedRecommendations(ctx, req.UserID); err == nil {
			slog.Info("Returning cached recommendations for user %s", req.UserID)
			return &models.RecommendationResponse{
				Items:     re.limitItems(cached, req.Limit),
				RequestID: requestID,
				Timestamp: time.Now(),
				Success:   true,
				Metadata: &models.Metadata{
					Algorithm:  "cached",
					Confidence: 0.8,
					Duration:   time.Since(startTime).String(),
				},
			}, nil
		}
	}

	// 获取用户画像
	profile, err := re.getOrCreateUserProfile(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	// 获取实时特征
	realTimeFeatures, _ := re.featureStore.GetRealTimeFeatures(ctx, req.UserID)

	// 融合用户特征
	fusedProfile := re.fuseUserFeatures(profile, realTimeFeatures)

	// 多策略推荐候选生成
	candidates, err := re.generateCandidates(ctx, fusedProfile, req)
	if err != nil {
		return nil, fmt.Errorf("failed to generate candidates: %w", err)
	}

	// 推荐去重
	deduplicatedItems, err := re.deduplicator.DeduplicateRecommendations(
		ctx, req.UserID, candidates, models.DedupeHybrid)
	if err != nil {
		slog.Error("Deduplication failed: %v", err)
		deduplicatedItems = candidates // 去重失败时使用原始候选
	}

	// 重排序和评分
	finalItems := re.rerank(deduplicatedItems, fusedProfile, req)

	// 限制返回数量
	finalItems = re.limitItems(finalItems, req.Limit)

	// 缓存结果（如果不是实时请求）
	if !req.RealTime && len(finalItems) > 0 {
		go re.cacheRecommendations(ctx, req.UserID, finalItems)
	}

	// 计算置信度
	confidence := re.calculateConfidence(finalItems, fusedProfile)

	// 记录推荐日志
	go re.logRecommendation(requestID, req, finalItems, time.Since(startTime))

	response := &models.RecommendationResponse{
		Items:     finalItems,
		RequestID: requestID,
		Timestamp: time.Now(),
		Success:   true,
		Metadata: &models.Metadata{
			Algorithm:   "hybrid_multilayer",
			Confidence:  confidence,
			Explanation: re.generateExplanation(finalItems, fusedProfile),
			Duration:    time.Since(startTime).String(),
		},
	}

	slog.Info("Completed recommendation request %s: %d items, confidence %.3f",
		requestID, len(finalItems), confidence)

	return response, nil
}

// generateCandidates 多策略候选生成
func (re *RecommendationEngine) generateCandidates(
	ctx context.Context,
	profile *models.UserProfile,
	req *models.RecommendationRequest,
) ([]*models.ContentItem, error) {

	var allCandidates []*models.ContentItem
	candidateLimit := req.Limit * 3 // 生成更多候选用于后续筛选

	// 1. 基于向量相似度的推荐
	if vectorCandidates, err := re.vectorBasedRecommendation(ctx, profile, req, candidateLimit/3); err == nil {
		slog.Info("Generated %d vector-based candidates", len(vectorCandidates))
		allCandidates = append(allCandidates, vectorCandidates...)
	} else {
		slog.Error("Vector-based recommendation failed: %v", err)
	}

	// 2. 热门内容推荐
	if popularCandidates, err := re.popularityBasedRecommendation(ctx, req, candidateLimit/3); err == nil {
		slog.Info("Generated %d popularity-based candidates", len(popularCandidates))
		allCandidates = append(allCandidates, popularCandidates...)
	} else {
		slog.Error("Popularity-based recommendation failed: %v", err)
	}

	// 3. 基于类别的推荐
	if categoryFilter, ok := req.Filters["category"].(string); ok {
		if categoryCandidates, err := re.categoryBasedRecommendation(ctx, profile, categoryFilter, candidateLimit/3); err == nil {
			slog.Info("Generated %d category-based candidates", len(categoryCandidates))
			allCandidates = append(allCandidates, categoryCandidates...)
		}
	}

	// 4. 如果候选太少，添加随机推荐作为补充
	if len(allCandidates) < req.Limit {
		if randomCandidates, err := re.randomRecommendation(ctx, req.Limit-len(allCandidates)); err == nil {
			slog.Info("Generated %d random candidates as fallback", len(randomCandidates))
			allCandidates = append(allCandidates, randomCandidates...)
		}
	}

	return allCandidates, nil
}

// vectorBasedRecommendation 向量检索推荐
func (re *RecommendationEngine) vectorBasedRecommendation(
	ctx context.Context,
	profile *models.UserProfile,
	req *models.RecommendationRequest,
	limit int,
) ([]*models.ContentItem, error) {

	return re.searchEngine.SearchSimilarContent(
		ctx,
		profile.Embedding,
		req.Filters,
		limit,
	)
}

// popularityBasedRecommendation 基于热门度推荐
func (re *RecommendationEngine) popularityBasedRecommendation(
	ctx context.Context,
	req *models.RecommendationRequest,
	limit int,
) ([]*models.ContentItem, error) {

	category := ""
	if categoryFilter, ok := req.Filters["category"].(string); ok {
		category = categoryFilter
	}

	return re.searchEngine.GetPopularContent(ctx, category, limit)
}

// categoryBasedRecommendation 基于类别推荐
func (re *RecommendationEngine) categoryBasedRecommendation(
	ctx context.Context,
	profile *models.UserProfile,
	category string,
	limit int,
) ([]*models.ContentItem, error) {

	// 根据用户在该类别的偏好进行推荐
	filters := map[string]interface{}{
		"category": category,
	}

	return re.searchEngine.SearchSimilarContent(
		ctx,
		profile.Embedding,
		filters,
		limit,
	)
}

// randomRecommendation 随机推荐（兜底策略）
func (re *RecommendationEngine) randomRecommendation(
	ctx context.Context,
	limit int,
) ([]*models.ContentItem, error) {

	return re.searchEngine.GetPopularContent(ctx, "", limit)
}

// rerank 重排序算法
func (re *RecommendationEngine) rerank(
	items []*models.ContentItem,
	profile *models.UserProfile,
	req *models.RecommendationRequest,
) []*models.ContentItem {

	// 为每个项目计算综合得分
	for _, item := range items {
		item.Score = re.calculateFinalScore(item, profile, req)
	}

	// 按分数排序
	sort.Slice(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})

	// 添加多样性调整
	return re.diversityRerank(items, profile)
}

// calculateFinalScore 计算最终得分
func (re *RecommendationEngine) calculateFinalScore(
	item *models.ContentItem,
	profile *models.UserProfile,
	req *models.RecommendationRequest,
) float64 {

	var score float64

	// 基础相关性得分 (40%)
	score += item.Score * 0.4

	// 用户偏好匹配度 (30%)
	prefScore := re.calculatePreferenceScore(item, profile)
	score += prefScore * 0.3

	// 新鲜度权重 (20%)
	freshnessScore := re.calculateFreshnessScore(item)
	score += freshnessScore * 0.2

	// 流行度权重 (10%)
	popularityScore := re.calculatePopularityScore(item)
	score += popularityScore * 0.1

	return math.Max(0, math.Min(1, score)) // 确保分数在0-1之间
}

// calculatePreferenceScore 计算用户偏好匹配度
func (re *RecommendationEngine) calculatePreferenceScore(
	item *models.ContentItem,
	profile *models.UserProfile,
) float64 {
	var score float64

	// 类别偏好
	if categoryPref, exists := profile.Categories[item.Category]; exists {
		score += categoryPref * 0.5
	}

	// 标签偏好
	tagScore := 0.0
	tagCount := 0
	for _, tag := range item.Tags {
		if tagPref, exists := profile.Tags[tag]; exists {
			tagScore += tagPref
			tagCount++
		}
	}
	if tagCount > 0 {
		score += (tagScore / float64(tagCount)) * 0.3
	}

	// 特征偏好
	featureScore := 0.0
	featureCount := 0
	for feature, value := range item.Features {
		if prefValue, exists := profile.Preferences[feature]; exists {
			// 计算特征相似度
			similarity := 1.0 - math.Abs(value-prefValue)
			featureScore += similarity
			featureCount++
		}
	}
	if featureCount > 0 {
		score += (featureScore / float64(featureCount)) * 0.2
	}

	return math.Max(0, math.Min(1, score))
}

// calculateFreshnessScore 计算新鲜度得分
func (re *RecommendationEngine) calculateFreshnessScore(item *models.ContentItem) float64 {
	now := time.Now().Unix()
	itemTime := item.CreatedAt

	// 计算时间差（小时）
	hoursDiff := float64(now-itemTime) / 3600.0

	// 新鲜度衰减函数：24小时内为1.0，然后指数衰减
	if hoursDiff <= 24 {
		return 1.0
	}

	// 指数衰减，半衰期为7天
	halfLife := 24.0 * 7 // 7天
	return math.Exp(-math.Ln2 * hoursDiff / halfLife)
}

// calculatePopularityScore 计算流行度得分
func (re *RecommendationEngine) calculatePopularityScore(item *models.ContentItem) float64 {
	// 从特征中获取流行度，如果没有则使用随机值
	if popularity, exists := item.Features["popularity"]; exists {
		return popularity
	}
	return rand.Float64() * 0.5 // 默认较低的流行度
}

// diversityRerank 多样性重排序
func (re *RecommendationEngine) diversityRerank(
	items []*models.ContentItem,
	profile *models.UserProfile,
) []*models.ContentItem {

	if len(items) <= 1 {
		return items
	}

	// MMR (Maximal Marginal Relevance) 算法简化版
	var result []*models.ContentItem
	remaining := make([]*models.ContentItem, len(items))
	copy(remaining, items)

	// lambda参数控制相关性vs多样性的权衡 (0.7表示更重视相关性)
	lambda := 0.7

	// 选择第一个最相关的项目
	if len(remaining) > 0 {
		result = append(result, remaining[0])
		remaining = remaining[1:]
	}

	// 迭代选择剩余项目
	for len(remaining) > 0 && len(result) < len(items) {
		bestIdx := 0
		bestScore := -1.0

		for i, candidate := range remaining {
			// 相关性得分
			relevanceScore := candidate.Score

			// 与已选择项目的最大相似度
			maxSimilarity := 0.0
			for _, selected := range result {
				similarity := re.calculateItemSimilarity(candidate, selected)
				if similarity > maxSimilarity {
					maxSimilarity = similarity
				}
			}

			// MMR得分
			mmrScore := lambda*relevanceScore - (1-lambda)*maxSimilarity

			if mmrScore > bestScore {
				bestScore = mmrScore
				bestIdx = i
			}
		}

		// 选择最佳项目
		result = append(result, remaining[bestIdx])
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}

	return result
}

// calculateItemSimilarity 计算项目间相似度
func (re *RecommendationEngine) calculateItemSimilarity(item1, item2 *models.ContentItem) float64 {
	var similarity float64

	// 类别相似度
	if item1.Category == item2.Category {
		similarity += 0.4
	}

	// 标签相似度
	commonTags := 0
	totalTags := len(item1.Tags) + len(item2.Tags)
	if totalTags > 0 {
		tagSet1 := make(map[string]bool)
		for _, tag := range item1.Tags {
			tagSet1[tag] = true
		}

		for _, tag := range item2.Tags {
			if tagSet1[tag] {
				commonTags++
			}
		}

		tagSimilarity := float64(commonTags*2) / float64(totalTags)
		similarity += tagSimilarity * 0.3
	}

	// 特征相似度
	if len(item1.Features) > 0 && len(item2.Features) > 0 {
		featureSim := re.calculateFeatureSimilarity(item1.Features, item2.Features)
		similarity += featureSim * 0.3
	}

	return math.Min(1.0, similarity)
}

// calculateFeatureSimilarity 计算特征向量相似度
func (re *RecommendationEngine) calculateFeatureSimilarity(
	features1, features2 map[string]float64,
) float64 {

	// 收集所有特征
	allFeatures := make(map[string]bool)
	for f := range features1 {
		allFeatures[f] = true
	}
	for f := range features2 {
		allFeatures[f] = true
	}

	if len(allFeatures) == 0 {
		return 0
	}

	// 计算余弦相似度
	var dotProduct, norm1, norm2 float64

	for feature := range allFeatures {
		val1, exists1 := features1[feature]
		if !exists1 {
			val1 = 0
		}

		val2, exists2 := features2[feature]
		if !exists2 {
			val2 = 0
		}

		dotProduct += val1 * val2
		norm1 += val1 * val1
		norm2 += val2 * val2
	}

	if norm1 == 0 || norm2 == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(norm1) * math.Sqrt(norm2))
}

// 辅助方法

func (re *RecommendationEngine) getOrCreateUserProfile(
	ctx context.Context,
	userID string,
) (*models.UserProfile, error) {

	// 尝试获取现有用户画像
	if profile, err := re.featureStore.GetUserProfile(ctx, userID); err == nil {
		return profile, nil
	}

	// 创建默认用户画像
	profile := re.createDefaultProfile(userID)

	// 保存默认画像
	if err := re.featureStore.SetUserProfile(ctx, profile); err != nil {
		slog.Error("Failed to save default profile for user %s: %v", userID, err)
	}

	return profile, nil
}

func (re *RecommendationEngine) createDefaultProfile(userID string) *models.UserProfile {
	return &models.UserProfile{
		UserID: userID,
		Preferences: map[string]float64{
			"popularity": 0.5,
			"quality":    0.7,
			"recency":    0.6,
			"engagement": 0.5,
			"relevance":  0.8,
		},
		Categories: map[string]float64{
			"tech":          0.3,
			"science":       0.2,
			"entertainment": 0.2,
			"sports":        0.1,
			"news":          0.2,
		},
		Tags:        make(map[string]float64),
		Embedding:   re.generateRandomEmbedding(128),
		LastUpdated: time.Now(),
	}
}

func (re *RecommendationEngine) generateRandomEmbedding(dim int) []float64 {
	embedding := make([]float64, dim)
	for i := range embedding {
		embedding[i] = rand.NormFloat64()
	}
	return embedding
}

func (re *RecommendationEngine) fuseUserFeatures(
	profile *models.UserProfile,
	realTimeFeatures *models.RealTimeFeatures,
) *models.UserProfile {

	if realTimeFeatures == nil {
		return profile
	}

	// 创建融合后的画像副本
	fusedProfile := &models.UserProfile{
		UserID:      profile.UserID,
		Preferences: make(map[string]float64),
		Categories:  make(map[string]float64),
		Tags:        make(map[string]float64),
		Embedding:   make([]float64, len(profile.Embedding)),
		LastUpdated: time.Now(),
	}

	// 复制基础偏好
	for k, v := range profile.Preferences {
		fusedProfile.Preferences[k] = v
	}
	for k, v := range profile.Categories {
		fusedProfile.Categories[k] = v
	}
	for k, v := range profile.Tags {
		fusedProfile.Tags[k] = v
	}
	copy(fusedProfile.Embedding, profile.Embedding)

	// 融合实时特征（简化实现）
	if len(realTimeFeatures.SessionData) > 0 {
		// 基于会话数据调整偏好
		for key, value := range realTimeFeatures.SessionData {
			if currentValue, exists := fusedProfile.Preferences[key]; exists {
				// 使用加权平均，实时特征权重为0.3
				fusedProfile.Preferences[key] = 0.7*currentValue + 0.3*value
			}
		}
	}

	return fusedProfile
}

func (re *RecommendationEngine) limitItems(items []*models.ContentItem, limit int) []*models.ContentItem {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func (re *RecommendationEngine) calculateConfidence(
	items []*models.ContentItem,
	profile *models.UserProfile,
) float64 {

	if len(items) == 0 {
		return 0.0
	}

	// 计算平均得分作为基础置信度
	totalScore := 0.0
	for _, item := range items {
		totalScore += item.Score
	}

	avgScore := totalScore / float64(len(items))

	// 根据推荐数量调整置信度
	countFactor := math.Min(1.0, float64(len(items))/float64(re.config.Recommend.DefaultLimit))

	confidence := avgScore*0.8 + countFactor*0.2

	return math.Max(0.1, math.Min(1.0, confidence))
}

func (re *RecommendationEngine) generateExplanation(
	items []*models.ContentItem,
	profile *models.UserProfile,
) map[string]string {

	explanations := make(map[string]string)

	if len(items) == 0 {
		explanations["reason"] = "No suitable recommendations found"
		return explanations
	}

	// 分析推荐原因
	categoryCount := make(map[string]int)
	for _, item := range items {
		categoryCount[item.Category]++
	}

	topCategory := ""
	maxCount := 0
	for category, count := range categoryCount {
		if count > maxCount {
			maxCount = count
			topCategory = category
		}
	}

	if topCategory != "" {
		explanations["primary_reason"] = fmt.Sprintf("Based on your interest in %s", topCategory)
		explanations["category_focus"] = topCategory
	}

	explanations["total_items"] = fmt.Sprintf("%d", len(items))
	explanations["algorithm"] = "Hybrid multi-layer recommendation with deduplication"

	return explanations
}

func (re *RecommendationEngine) cacheRecommendations(
	ctx context.Context,
	userID string,
	items []*models.ContentItem,
) {

	if err := re.featureStore.CacheRecommendations(ctx, userID, items); err != nil {
		slog.Error("Failed to cache recommendations for user %s: %v", userID, err)
	}
}

func (re *RecommendationEngine) logRecommendation(
	requestID string,
	req *models.RecommendationRequest,
	items []*models.ContentItem,
	duration time.Duration,
) {

	slog.Info("Recommendation completed - RequestID: %s, UserID: %s, Items: %d, Duration: %s",
		requestID, req.UserID, len(items), duration)
}

func (re *RecommendationEngine) generateRequestID() string {
	return uuid.New().String()
}

// RecordUserAction 记录用户行为
func (re *RecommendationEngine) RecordUserAction(
	ctx context.Context,
	req *models.UserActionRequest,
) error {

	slog.Info("Recording user action: %s by user %s on content %s",
		req.Action, req.UserID, req.ContentID)

	// 记录用户行为
	if err := re.featureStore.RecordUserAction(ctx, req.UserID, req.Action, req.ContentID, req.Context); err != nil {
		return fmt.Errorf("failed to record user action: %w", err)
	}

	// 异步更新用户画像
	go re.updateUserProfileAsync(ctx, req)

	return nil
}

func (re *RecommendationEngine) updateUserProfileAsync(
	ctx context.Context,
	req *models.UserActionRequest,
) {

	// 获取当前用户画像
	profile, err := re.featureStore.GetUserProfile(ctx, req.UserID)
	if err != nil {
		slog.Error("Failed to get user profile for update: %v", err)
		return
	}

	// 根据用户行为更新画像（简化实现）
	if req.Action == "like" {
		// 增加相关偏好
		if profile.Preferences == nil {
			profile.Preferences = make(map[string]float64)
		}
		profile.Preferences["quality"] = math.Min(1.0, profile.Preferences["quality"]+0.1)
		profile.Preferences["relevance"] = math.Min(1.0, profile.Preferences["relevance"]+0.1)
	}

	// 保存更新的画像
	if err := re.featureStore.SetUserProfile(ctx, profile); err != nil {
		slog.Error("Failed to update user profile: %v", err)
	}
}

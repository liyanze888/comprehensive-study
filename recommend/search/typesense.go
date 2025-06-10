package search

import (
	"comprehensive-study/recommend/models"
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strings"
)

// MockTypesenseClient 模拟 Typesense 客户端
type MockTypesenseClient struct {
	mockData []*models.ContentItem
}

func NewMockTypesenseClient() *MockTypesenseClient {
	return &MockTypesenseClient{
		mockData: generateMockData(),
	}
}

// SearchSimilarContent 搜索相似内容
func (tc *MockTypesenseClient) SearchSimilarContent(
	ctx context.Context,
	userEmbedding []float64,
	filters map[string]interface{},
	limit int,
) ([]*models.ContentItem, error) {

	slog.Info("Found %d similar items", slog.Any("len(userEmbedding)", len(userEmbedding)))

	var results []*models.ContentItem

	// 简单的模拟搜索逻辑
	for _, item := range tc.mockData {
		// 应用过滤器
		if !tc.matchesFilters(item, filters) {
			continue
		}

		// 计算相似度（简化版）
		similarity := tc.calculateSimilarity(userEmbedding, item.Embedding)
		item.Score = similarity

		results = append(results, &models.ContentItem{
			ID:          item.ID,
			Title:       item.Title,
			Description: item.Description,
			Category:    item.Category,
			Tags:        item.Tags,
			Features:    item.Features,
			CreatedAt:   item.CreatedAt,
			Score:       similarity,
			Reason:      "Similar to your preferences",
		})

		if len(results) >= limit {
			break
		}
	}

	slog.Info("Found %d similar items", slog.Any("length", len(results)))
	return results, nil
}

// HybridSearch 混合搜索
func (tc *MockTypesenseClient) HybridSearch(
	ctx context.Context,
	query string,
	userEmbedding []float64,
	userProfile *models.UserProfile,
	limit int,
) ([]*models.ContentItem, error) {

	var results []*models.ContentItem

	// 简单的文本匹配 + 向量搜索
	for _, item := range tc.mockData {
		textScore := tc.calculateTextSimilarity(query, item)
		vectorScore := tc.calculateSimilarity(userEmbedding, item.Embedding)

		// 混合评分
		finalScore := 0.6*vectorScore + 0.4*textScore

		if finalScore > 0.3 { // 阈值过滤
			results = append(results, &models.ContentItem{
				ID:          item.ID,
				Title:       item.Title,
				Description: item.Description,
				Category:    item.Category,
				Tags:        item.Tags,
				Features:    item.Features,
				CreatedAt:   item.CreatedAt,
				Score:       finalScore,
				Reason:      fmt.Sprintf("Matches your search: %s", query),
			})
		}

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// GetPopularContent 获取热门内容
func (tc *MockTypesenseClient) GetPopularContent(
	ctx context.Context,
	category string,
	limit int,
) ([]*models.ContentItem, error) {

	var results []*models.ContentItem

	for _, item := range tc.mockData {
		if category == "" || item.Category == category {
			results = append(results, &models.ContentItem{
				ID:          item.ID,
				Title:       item.Title,
				Description: item.Description,
				Category:    item.Category,
				Tags:        item.Tags,
				Features:    item.Features,
				CreatedAt:   item.CreatedAt,
				Score:       rand.Float64(), // 模拟热门分数
				Reason:      "Popular content",
			})
		}

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// 辅助方法
func (tc *MockTypesenseClient) matchesFilters(item *models.ContentItem, filters map[string]interface{}) bool {
	if filters == nil {
		return true
	}

	if category, ok := filters["category"].(string); ok {
		if item.Category != category {
			return false
		}
	}

	return true
}

func (tc *MockTypesenseClient) calculateSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return rand.Float64() * 0.5 // 随机相似度
	}

	// 余弦相似度
	var dotProduct, normA, normB float64
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}

	for i := 0; i < minLen; i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

func (tc *MockTypesenseClient) calculateTextSimilarity(query string, item *models.ContentItem) float64 {
	query = strings.ToLower(query)
	text := strings.ToLower(item.Title + " " + item.Description)

	if strings.Contains(text, query) {
		return 0.8
	}

	// 简单的词匹配
	queryWords := strings.Fields(query)
	textWords := strings.Fields(text)

	matchCount := 0
	for _, qw := range queryWords {
		for _, tw := range textWords {
			if strings.Contains(tw, qw) || strings.Contains(qw, tw) {
				matchCount++
				break
			}
		}
	}

	if len(queryWords) == 0 {
		return 0
	}

	return float64(matchCount) / float64(len(queryWords))
}

// 生成模拟数据
func generateMockData() []*models.ContentItem {
	categories := []string{"tech", "science", "entertainment", "sports", "news"}

	var items []*models.ContentItem

	for i := 1; i <= 100; i++ {
		category := categories[rand.Intn(len(categories))]

		item := &models.ContentItem{
			ID:          fmt.Sprintf("content_%d", i),
			Title:       fmt.Sprintf("%s Content %d", strings.Title(category), i),
			Description: fmt.Sprintf("This is a %s content description for item %d", category, i),
			Category:    category,
			Tags:        generateRandomTags(category),
			Features:    generateRandomFeatures(),
			Embedding:   generateRandomEmbedding(128),
			CreatedAt:   int64(1700000000 + i*3600), // 模拟时间戳
		}

		items = append(items, item)
	}

	return items
}

func generateRandomTags(category string) []string {
	tagSets := map[string][]string{
		"tech":          {"programming", "ai", "web", "mobile", "cloud"},
		"science":       {"research", "biology", "physics", "chemistry", "math"},
		"entertainment": {"movie", "music", "game", "tv", "celebrity"},
		"sports":        {"football", "basketball", "tennis", "soccer", "olympics"},
		"news":          {"politics", "economy", "world", "local", "breaking"},
	}

	tags := tagSets[category]
	if tags == nil {
		tags = []string{"general", "content", "popular"}
	}

	var result []string
	for i := 0; i < rand.Intn(3)+1; i++ {
		if i < len(tags) {
			result = append(result, tags[i])
		}
	}

	return result
}

func generateRandomFeatures() map[string]float64 {
	return map[string]float64{
		"popularity": rand.Float64(),
		"quality":    rand.Float64(),
		"recency":    rand.Float64(),
		"engagement": rand.Float64(),
		"relevance":  rand.Float64(),
	}
}

func generateRandomEmbedding(dim int) []float64 {
	embedding := make([]float64, dim)
	for i := range embedding {
		embedding[i] = rand.NormFloat64()
	}
	return embedding
}

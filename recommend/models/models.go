package models

import (
	"time"
)

// ContentItem 内容项目
type ContentItem struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Category    string             `json:"category"`
	Tags        []string           `json:"tags"`
	Features    map[string]float64 `json:"features"`
	Embedding   []float64          `json:"embedding,omitempty"`
	CreatedAt   int64              `json:"created_at"`
	Score       float64            `json:"score,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

// UserProfile 用户画像
type UserProfile struct {
	UserID      string             `json:"user_id"`
	Preferences map[string]float64 `json:"preferences"`
	Categories  map[string]float64 `json:"categories"`
	Tags        map[string]float64 `json:"tags"`
	Embedding   []float64          `json:"embedding"`
	LastUpdated time.Time          `json:"last_updated"`
}

// RealTimeFeatures 实时特征
type RealTimeFeatures struct {
	ViewHistory   []string           `json:"view_history"`
	LikeHistory   []string           `json:"like_history"`
	SearchHistory []string           `json:"search_history"`
	SessionData   map[string]float64 `json:"session_data"`
	Timestamp     time.Time          `json:"timestamp"`
}

// ContentFingerprint 内容指纹（去重用）
type ContentFingerprint struct {
	ID          string    `json:"id"`
	ContentHash string    `json:"content_hash"`
	FeatureHash string    `json:"feature_hash"`
	Similarity  float64   `json:"similarity"`
	LastSeen    time.Time `json:"last_seen"`
}

// RecommendationRequest 推荐请求
type RecommendationRequest struct {
	UserID   string                 `json:"user_id" binding:"required"`
	Context  map[string]interface{} `json:"context"`
	Filters  map[string]interface{} `json:"filters"`
	Limit    int                    `json:"limit"`
	Strategy string                 `json:"strategy"`
	RealTime bool                   `json:"real_time"`
}

// RecommendationResponse 推荐响应
type RecommendationResponse struct {
	Items     []*ContentItem `json:"items"`
	Metadata  *Metadata      `json:"metadata"`
	RequestID string         `json:"request_id"`
	Timestamp time.Time      `json:"timestamp"`
	Success   bool           `json:"success"`
	Message   string         `json:"message,omitempty"`
}

// Metadata 元数据
type Metadata struct {
	Algorithm   string                 `json:"algorithm"`
	Confidence  float64                `json:"confidence"`
	Explanation map[string]string      `json:"explanation"`
	Debug       map[string]interface{} `json:"debug,omitempty"`
	Duration    string                 `json:"duration"`
}

// DedupeStrategy 去重策略
type DedupeStrategy int

const (
	DedupeByID DedupeStrategy = iota
	DedupeByContent
	DedupeByFeatures
	DedupeHybrid
)

// UserActionRequest 用户行为请求
type UserActionRequest struct {
	UserID    string                 `json:"user_id" binding:"required"`
	ContentID string                 `json:"content_id" binding:"required"`
	Action    string                 `json:"action" binding:"required"` // view, like, share, etc.
	Context   map[string]interface{} `json:"context"`
}

// APIResponse 通用API响应
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
	Error   string      `json:"error,omitempty"`
}

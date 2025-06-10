package config

import (
	"time"
)

type Config struct {
	Server    ServerConfig    `json:"server"`
	Redis     RedisConfig     `json:"redis"`
	Typesense TypesenseConfig `json:"typesense"`
	Cache     CacheConfig     `json:"cache"`
	Dedup     DedupeConfig    `json:"dedup"`
	Recommend RecommendConfig `json:"recommend"`
}

type ServerConfig struct {
	Port string `json:"port"`
	Mode string `json:"mode"` // debug, release
}

type RedisConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Password string `json:"password"`
	DB       int    `json:"db"`
}

type TypesenseConfig struct {
	Host   string `json:"host"`
	APIKey string `json:"api_key"`
}

type CacheConfig struct {
	UserProfileTTL    time.Duration `json:"user_profile_ttl"`
	RecommendationTTL time.Duration `json:"recommendation_ttl"`
	FeatureTTL        time.Duration `json:"feature_ttl"`
}

type DedupeConfig struct {
	WindowSize          time.Duration `json:"window_size"`
	SimilarityThreshold float64       `json:"similarity_threshold"`
	MaxCacheSize        int           `json:"max_cache_size"`
	ContentTTL          time.Duration `json:"content_ttl"`
}

type RecommendConfig struct {
	DefaultLimit        int     `json:"default_limit"`
	MaxLimit            int     `json:"max_limit"`
	ConfidenceThreshold float64 `json:"confidence_threshold"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: "8080",
			Mode: "debug",
		},
		Redis: RedisConfig{
			Host:     "localhost",
			Port:     "6379",
			Password: "",
			DB:       0,
		},
		Typesense: TypesenseConfig{
			Host:   "http://localhost:8108",
			APIKey: "xyz",
		},
		Cache: CacheConfig{
			UserProfileTTL:    24 * time.Hour,
			RecommendationTTL: 2 * time.Hour,
			FeatureTTL:        6 * time.Hour,
		},
		Dedup: DedupeConfig{
			WindowSize:          12 * time.Hour,
			SimilarityThreshold: 0.85,
			MaxCacheSize:        1000,
			ContentTTL:          48 * time.Hour,
		},
		Recommend: RecommendConfig{
			DefaultLimit:        10,
			MaxLimit:            50,
			ConfidenceThreshold: 0.7,
		},
	}
}

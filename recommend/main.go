package main

import (
	"comprehensive-study/recommend/cache"
	"comprehensive-study/recommend/config"
	"comprehensive-study/recommend/dedup"
	"comprehensive-study/recommend/engine"
	"comprehensive-study/recommend/models"
	"comprehensive-study/recommend/search"
	"context"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	// 初始化配置
	cfg := config.DefaultConfig()

	// 初始化日志

	// 初始化 Redis 客户端
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Host + ":" + cfg.Redis.Port,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// 测试 Redis 连接
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("Redis connection failed: %v (using mock mode)", err)
	} else {
		slog.Info("Redis connected successfully")
	}

	// 初始化组件
	searchEngine := search.NewMockTypesenseClient()
	featureStore := cache.NewFeatureStore(&cfg.Redis, &cfg.Cache)
	deduplicator := dedup.NewDeduplicator(rdb, &cfg.Dedup)

	// 创建推荐引擎
	recommendEngine := engine.NewRecommendationEngine(
		searchEngine,
		featureStore,
		deduplicator,
		cfg,
	)

	// 设置 Gin 模式
	gin.SetMode(cfg.Server.Mode)

	// 创建路由
	r := gin.Default()

	// 中间件
	r.Use(corsMiddleware())
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, models.APIResponse{
			Success: true,
			Data:    map[string]string{"status": "healthy"},
			Message: "Service is running",
		})
	})

	// API 路由
	api := r.Group("/api/v1")
	{
		// 获取推荐
		api.POST("/recommendations", handleGetRecommendations(recommendEngine))

		// 记录用户行为
		api.POST("/user-actions", handleUserAction(recommendEngine))

		// 获取用户画像
		api.GET("/users/:user_id/profile", handleGetUserProfile(featureStore))

		// 更新用户画像
		api.PUT("/users/:user_id/profile", handleUpdateUserProfile(featureStore))

		// 系统状态
		api.GET("/status", handleSystemStatus(featureStore))
	}

	slog.Info("Starting server on port %s", cfg.Server.Port)
	if err := r.Run(":" + cfg.Server.Port); err != nil {
		slog.Error("Failed to start server: %v", err)
	}
}

// 处理推荐请求
func handleGetRecommendations(engine *engine.RecommendationEngine) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req models.RecommendationRequest

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, models.APIResponse{
				Success: false,
				Error:   "Invalid request format: " + err.Error(),
			})
			return
		}

		// 参数验证
		if req.UserID == "" {
			c.JSON(http.StatusBadRequest, models.APIResponse{
				Success: false,
				Error:   "user_id is required",
			})
			return
		}

		ctx := c.Request.Context()
		response, err := engine.GetRecommendations(ctx, &req)
		if err != nil {
			slog.Error("Recommendation failed: %v", err)
			c.JSON(http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Error:   "Failed to generate recommendations",
			})
			return
		}

		c.JSON(http.StatusOK, response)
	}
}

// 处理用户行为记录
func handleUserAction(engine *engine.RecommendationEngine) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req models.UserActionRequest

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, models.APIResponse{
				Success: false,
				Error:   "Invalid request format: " + err.Error(),
			})
			return
		}

		ctx := c.Request.Context()
		if err := engine.RecordUserAction(ctx, &req); err != nil {
			slog.Error("Failed to record user action: %v", err)
			c.JSON(http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Error:   "Failed to record user action",
			})
			return
		}

		c.JSON(http.StatusOK, models.APIResponse{
			Success: true,
			Message: "User action recorded successfully",
		})
	}
}

// 获取用户画像
func handleGetUserProfile(featureStore *cache.FeatureStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.Param("user_id")
		if userID == "" {
			c.JSON(http.StatusBadRequest, models.APIResponse{
				Success: false,
				Error:   "user_id is required",
			})
			return
		}

		ctx := c.Request.Context()
		profile, err := featureStore.GetUserProfile(ctx, userID)
		if err != nil {
			c.JSON(http.StatusNotFound, models.APIResponse{
				Success: false,
				Error:   "User profile not found",
			})
			return
		}

		c.JSON(http.StatusOK, models.APIResponse{
			Success: true,
			Data:    profile,
		})
	}
}

// 更新用户画像
func handleUpdateUserProfile(featureStore *cache.FeatureStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.Param("user_id")
		if userID == "" {
			c.JSON(http.StatusBadRequest, models.APIResponse{
				Success: false,
				Error:   "user_id is required",
			})
			return
		}

		var profile models.UserProfile
		if err := c.ShouldBindJSON(&profile); err != nil {
			c.JSON(http.StatusBadRequest, models.APIResponse{
				Success: false,
				Error:   "Invalid profile format: " + err.Error(),
			})
			return
		}

		profile.UserID = userID

		ctx := c.Request.Context()
		if err := featureStore.SetUserProfile(ctx, &profile); err != nil {
			slog.Error("Failed to update user profile: %v", err)
			c.JSON(http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Error:   "Failed to update user profile",
			})
			return
		}

		c.JSON(http.StatusOK, models.APIResponse{
			Success: true,
			Message: "User profile updated successfully",
		})
	}
}

// 系统状态检查
func handleSystemStatus(featureStore *cache.FeatureStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		status := map[string]interface{}{
			"timestamp": "time.Now()",
			"service":   "comprehensive-study/recommend",
			"version":   "1.0.0",
		}

		// 检查 Redis 连接
		if err := featureStore.Ping(ctx); err != nil {
			status["redis"] = "disconnected"
			status["redis_error"] = err.Error()
		} else {
			status["redis"] = "connected"
		}

		c.JSON(http.StatusOK, models.APIResponse{
			Success: true,
			Data:    status,
		})
	}
}

// CORS 中间件
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Header("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

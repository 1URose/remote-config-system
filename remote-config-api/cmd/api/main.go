package main

import (
	"log/slog"
	"os"

	"github.com/redis/go-redis/v9"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/api"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/auth"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/config"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/metrics"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/repository"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/service"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))

	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPass,
		DB:       cfg.RedisDB,
	})

	metricsRegistry := metrics.NewRegistry()
	repo := repository.NewRedisRepository(redisClient, cfg.AuditLimit)
	svc := service.NewConfigService(repo, metricsRegistry)
	authManager := auth.NewManager(cfg.JWTSecret)
	server := api.NewServer(cfg.HTTPAddr, logger, svc, authManager, metricsRegistry)

	if err := server.ListenAndServe(); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}


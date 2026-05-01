package config

import (
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr   string
	RedisAddr  string
	RedisDB    int
	RedisPass  string
	JWTSecret  string
	LogLevel   string
	AuditLimit int64
}

func Load() Config {
	return Config{
		HTTPAddr:   getEnv("REMOTE_CONFIG_HTTP_ADDR", ":8080"),
		RedisAddr:  getEnv("REMOTE_CONFIG_REDIS_ADDR", "localhost:6379"),
		RedisDB:    getEnvInt("REMOTE_CONFIG_REDIS_DB", 0),
		RedisPass:  os.Getenv("REMOTE_CONFIG_REDIS_PASSWORD"),
		JWTSecret:  getEnv("REMOTE_CONFIG_JWT_SECRET", "dev-secret"),
		LogLevel:   getEnv("REMOTE_CONFIG_LOG_LEVEL", "info"),
		AuditLimit: int64(getEnvInt("REMOTE_CONFIG_AUDIT_LIMIT", 200)),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}


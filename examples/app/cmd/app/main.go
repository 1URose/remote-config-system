package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
	"github.com/ya.ermakov/remote-config-system/remoteconfig"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	namespace := getenv("APP_NAMESPACE", "payments")
	key := getenv("APP_KEY", "feature_x_enabled")
	namespaces := splitCSV(getenv("APP_NAMESPACES", namespace))
	pollInterval, _ := time.ParseDuration(getenv("APP_POLL_INTERVAL", "5s"))

	client, err := remoteconfig.New(context.Background(), remoteconfig.Options{
		RedisAddr:     getenv("APP_REDIS_ADDR", "localhost:6379"),
		RedisPassword: os.Getenv("APP_REDIS_PASSWORD"),
		Namespaces:    namespaces,
		Logger:        logger,
		RetryInterval: 2 * time.Second,
	})
	if err != nil {
		logger.Error("failed to create remote config client", "error", err)
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	client.Watch(namespace, key, func(item model.ConfigItem) {
		logger.Info("watch callback fired", "namespace", item.Namespace, "key", item.Key, "value", item.Value, "version", item.Version)
	})

	client.WatchNamespace(namespace, func(items []model.ConfigItem) {
		logger.Info("namespace callback fired", "namespace", namespace, "changed", len(items), "cache_size", client.CacheSize())
	})

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	logger.Info("application started", "namespace", namespace, "key", key, "redis_connected", client.RedisConnected())
	printValue(logger, client, namespace, key)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			printValue(logger, client, namespace, key)
		case sig := <-sigCh:
			logger.Info("shutting down application", "signal", sig.String())
			return
		}
	}
}

func printValue(logger *slog.Logger, client *remoteconfig.Client, namespace, key string) {
	item, err := client.GetRaw(namespace, key)
	if err != nil {
		logger.Warn("config not available yet", "namespace", namespace, "key", key, "error", err)
		return
	}
	logger.Info("current config value", "namespace", namespace, "key", key, "value", item.Value, "version", item.Version, "redis_connected", client.RedisConnected())
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

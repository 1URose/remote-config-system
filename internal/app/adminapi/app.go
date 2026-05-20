package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	docs "github.com/1URose/remote-config-system/docs"
	"github.com/1URose/remote-config-system/internal/auth"
	appconfig "github.com/1URose/remote-config-system/internal/config"
	handlerhttp "github.com/1URose/remote-config-system/internal/handlers/http"
	"github.com/1URose/remote-config-system/internal/metrics"
	configservice "github.com/1URose/remote-config-system/internal/services/config"
	featureservice "github.com/1URose/remote-config-system/internal/services/feature"
	redisstorage "github.com/1URose/remote-config-system/internal/storage/redis"
)

type App struct {
	logger      *slog.Logger
	server      *http.Server
	redisClient interface{ Close() error }
}

func New(cfg appconfig.Config) *App {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))
	docs.SwaggerInfo.Host = swaggerHost(cfg.HTTPAddr)
	docs.SwaggerInfo.BasePath = "/"

	redisClient := redisstorage.NewClient(redisstorage.ClientConfig{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPass,
		DB:       cfg.RedisDB,
	})

	metricsRegistry := metrics.NewRegistry()
	configStorage := redisstorage.NewConfigStorage(redisClient, cfg.AuditLimit)
	featureStorage := redisstorage.NewFeatureStorage(redisClient, cfg.AuditLimit)
	publisher := redisstorage.NewPubSub(redisClient)
	service := configservice.NewService(configStorage, publisher, metricsRegistry)
	featureService := featureservice.NewService(featureStorage)
	authManager := auth.NewManager(cfg.JWTSecret)
	handler := handlerhttp.NewServer(logger, service, featureService, authManager, metricsRegistry)

	return &App{
		logger: logger,
		server: &http.Server{
			Addr:    cfg.HTTPAddr,
			Handler: handler.Handler(),
		},
		redisClient: redisClient,
	}
}

func (a *App) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.server.Shutdown(shutdownCtx)
	}()

	a.logger.Info("starting admin api", "addr", a.server.Addr)
	err := a.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Close() error {
	return a.redisClient.Close()
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

func swaggerHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "localhost:8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

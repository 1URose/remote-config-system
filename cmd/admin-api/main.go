// @title Remote Config System Admin API
// @version 1.0
// @description Admin API для управления конфигурациями и feature toggles с поддержкой hot-reload.
// @description Значения сохраняются в Redis, после чего публикуется событие обновления через Redis Pub/Sub.
// @host localhost:8080
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Введите токен в формате: Bearer <token>
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/1URose/remote-config-system/docs"
	"github.com/1URose/remote-config-system/internal/app/adminapi"
	appconfig "github.com/1URose/remote-config-system/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := adminapi.New(appconfig.Load())
	defer func() { _ = app.Close() }()

	if err := app.Run(ctx); err != nil {
		slog.Error("admin api exited", "error", err)
		os.Exit(1)
	}
}

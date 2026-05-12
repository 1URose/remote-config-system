package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/1URose/remote-config-system/pkg/sdk"
)

const (
	httpAddr  = ":8081"
	namespace = "demo-service"
)

type stateResponse struct {
	Title            string `json:"title"`
	Theme            string `json:"theme"`
	DiscountPercent  int    `json:"discount_percent"`
	NewBannerEnabled bool   `json:"new_banner_enabled"`
	CheckoutEnabled  bool   `json:"checkout_enabled"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Printf("demo service stopped with error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	log.Println("demo service starting")

	client, err := sdk.NewClient(
		sdk.WithRedisAddr("localhost:6379"),
	)
	if err != nil {
		return fmt.Errorf("create sdk client: %w", err)
	}
	log.Println("sdk client created")
	defer func() { _ = client.Close() }()
	config := client.Namespace(namespace)

	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start sdk client: %w", err)
	}
	log.Println("sdk started")

	webDir, err := resolveWebDir()
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:    httpAddr,
		Handler: newHandler(config, webDir),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Println("demo service listening on :8081")

	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newHandler(config *sdk.Namespace, webDir string) http.Handler {
	mux := http.NewServeMux()
	fileServer := http.FileServer(http.Dir(webDir))

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		log.Println("state requested")
		writeJSON(w, http.StatusOK, readState(config))
	})
	mux.Handle("GET /", fileServer)

	return mux
}

func readState(config *sdk.Namespace) stateResponse {
	return stateResponse{
		Title:            getStringOrDefault(config, "app.title", "Remote Config Demo"),
		Theme:            getStringOrDefault(config, "app.theme", "light"),
		DiscountPercent:  getIntOrDefault(config, "discount.percent", 10),
		NewBannerEnabled: getFeatureOrDefault(config, "new_banner", true),
		CheckoutEnabled:  getFeatureOrDefault(config, "checkout_enabled", false),
	}
}

func getStringOrDefault(config *sdk.Namespace, key, fallback string) string {
	value, ok := config.GetString(key)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func getIntOrDefault(config *sdk.Namespace, key string, fallback int) int {
	value, ok := config.GetInt(key)
	if !ok {
		return fallback
	}
	return value
}

func getFeatureOrDefault(config *sdk.Namespace, key string, fallback bool) bool {
	feature, ok := config.GetFeature(key)
	if !ok {
		return fallback
	}
	return feature.Enabled
}

func resolveWebDir() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve source path: runtime caller unavailable")
	}

	webDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "web"))
	info, err := os.Stat(webDir)
	if err != nil {
		return "", fmt.Errorf("resolve web dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("resolve web dir: %s is not a directory", webDir)
	}
	return webDir, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdhttp "net/http"
	"strings"

	"gopkg.in/yaml.v3"

	_ "github.com/1URose/remote-config-system/docs"
	"github.com/1URose/remote-config-system/internal/auth"
	"github.com/1URose/remote-config-system/internal/domain"
	"github.com/1URose/remote-config-system/internal/metrics"
	auditservice "github.com/1URose/remote-config-system/internal/services/audit"
	configservice "github.com/1URose/remote-config-system/internal/services/config"
	featureservice "github.com/1URose/remote-config-system/internal/services/feature"
	httpSwagger "github.com/swaggo/http-swagger"
)

type Server struct {
	logger         *slog.Logger
	configService  *configservice.Service
	featureService *featureservice.Service
	auth           *auth.Manager
	metrics        *metrics.Registry
}

func NewServer(logger *slog.Logger, configSvc *configservice.Service, featureSvc *featureservice.Service, authManager *auth.Manager, metricsRegistry *metrics.Registry) *Server {
	return &Server{
		logger:         logger,
		configService:  configSvc,
		featureService: featureSvc,
		auth:           authManager,
		metrics:        metricsRegistry,
	}
}

func (s *Server) Handler() stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /docs", s.handleDocsRedirect)
	mux.HandleFunc("GET /docs/", s.handleDocsRedirect)
	mux.Handle("GET /metrics", stdhttp.HandlerFunc(s.handleMetrics))
	mux.Handle("/swagger/", httpSwagger.WrapHandler)
	mux.HandleFunc("GET /configs/{namespace}/{key}", s.handleGetConfigKey)
	mux.HandleFunc("PUT /configs/{namespace}/{key}", s.handlePutConfigKey)
	mux.HandleFunc("DELETE /configs/{namespace}/{key}", s.handleDeleteConfigKey)
	mux.HandleFunc("GET /features/{namespace}/{key}", s.handleGetFeatureKey)
	mux.HandleFunc("PUT /features/{namespace}/{key}", s.handlePutFeatureKey)
	mux.HandleFunc("DELETE /features/{namespace}/{key}", s.handleDeleteFeatureKey)
	mux.Handle("GET /config", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleGetConfig)))
	mux.Handle("POST /config/update", s.auth.Middleware(auth.RoleEditor, stdhttp.HandlerFunc(s.handleUpdate)))
	mux.Handle("POST /cache/flush", s.auth.Middleware(auth.RoleOwner, stdhttp.HandlerFunc(s.handleFlush)))
	mux.Handle("POST /config/import", s.auth.Middleware(auth.RoleOwner, stdhttp.HandlerFunc(s.handleImport)))
	mux.Handle("GET /config/export", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleExport)))
	mux.Handle("GET /audit", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleAudit)))
	return s.withRequestContext(mux)
}

func (s *Server) withRequestContext(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = randomRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Health godoc
// @Summary Проверка состояния Admin API
// @Description Возвращает статус работы сервиса и доступность Redis.
// @Tags health
// @Produce json
// @Success 200 {object} HealthResponse
// @Failure 503 {object} HealthResponse
// @Router /health [get]
func (s *Server) handleHealth(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if err := s.configService.Health(r.Context()); err != nil {
		s.writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]any{
			"status": "degraded",
			"redis":  "down",
			"error":  err.Error(),
		})
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, map[string]any{
		"status": "ok",
		"redis":  "up",
	})
}

func (s *Server) handleDocsRedirect(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	stdhttp.Redirect(w, r, "/swagger/index.html", stdhttp.StatusTemporaryRedirect)
}

// Metrics godoc
// @Summary Метрики Admin API
// @Description Возвращает метрики в формате Prometheus.
// @Tags metrics
// @Produce plain
// @Success 200 {string} string "Prometheus metrics"
// @Router /metrics [get]
func (s *Server) handleMetrics(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	s.metrics.Handler().ServeHTTP(w, r)
}

// GetConfigNamespace godoc
// @Summary Получить все конфиги namespace
// @Description Возвращает все конфигурационные параметры для указанного namespace.
// @Tags legacy-config
// @Produce json
// @Security BearerAuth
// @Param namespace query string true "Namespace сервиса"
// @Success 200 {object} ConfigNamespaceResponse
// @Failure 400 {string} string "namespace is required"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 500 {string} string "internal server error"
// @Router /config [get]
func (s *Server) handleGetConfig(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		stdhttp.Error(w, "namespace is required", stdhttp.StatusBadRequest)
		return
	}
	items, err := s.configService.GetNamespace(r.Context(), namespace)
	if err != nil {
		s.internalError(w, "get namespace", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, map[string]any{
		"namespace": namespace,
		"items":     items,
	})
}

// GetConfigKey godoc
// @Summary Получить конфигурационный параметр
// @Description Возвращает конфигурационный параметр по namespace и key.
// @Tags configs
// @Produce json
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ параметра"
// @Success 200 {object} domain.ConfigItem
// @Failure 404 {string} string "config item not found"
// @Failure 500 {string} string "internal server error"
// @Router /configs/{namespace}/{key} [get]
func (s *Server) handleGetConfigKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	item, err := s.configService.GetKey(r.Context(), strings.TrimSpace(r.PathValue("namespace")), strings.TrimSpace(r.PathValue("key")))
	if err != nil {
		s.writeDomainError(w, "get config key", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, item)
}

// PutConfigKey godoc
// @Summary Создать или обновить конфигурационный параметр
// @Description Сохраняет конфигурационный параметр в Redis и публикует событие обновления для SDK.
// @Tags configs
// @Accept json
// @Produce json
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ параметра"
// @Param request body PutConfigKeyRequest true "Данные конфигурационного параметра"
// @Success 200 {object} domain.ConfigItem
// @Failure 400 {string} string "bad request or validation error"
// @Failure 409 {string} string "version conflict"
// @Failure 500 {string} string "internal server error"
// @Router /configs/{namespace}/{key} [put]
func (s *Server) handlePutConfigKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req PutConfigKeyRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	req.UpdatedBy = defaultUpdatedBy(req.UpdatedBy)

	item, err := s.configService.UpsertKey(
		r.Context(),
		strings.TrimSpace(r.PathValue("namespace")),
		strings.TrimSpace(r.PathValue("key")),
		req.Value,
		req.Type,
		req.ExpectedVersion,
		req.IsSecret,
		req.UpdatedBy,
		requestIDFromContext(r.Context()),
	)
	if err != nil {
		s.writeDomainError(w, "put config key", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, item)
}

// DeleteConfigKey godoc
// @Summary Удалить конфигурационный параметр
// @Description Удаляет параметр из Redis и публикует событие обновления.
// @Tags configs
// @Produce json
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ параметра"
// @Param updatedBy query string false "Кто выполняет удаление"
// @Success 204
// @Failure 400 {string} string "validation error"
// @Failure 404 {string} string "config item not found"
// @Failure 500 {string} string "internal server error"
// @Router /configs/{namespace}/{key} [delete]
func (s *Server) handleDeleteConfigKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	updatedBy := defaultUpdatedBy(r.URL.Query().Get("updatedBy"))
	err := s.configService.DeleteKey(
		r.Context(),
		strings.TrimSpace(r.PathValue("namespace")),
		strings.TrimSpace(r.PathValue("key")),
		updatedBy,
		requestIDFromContext(r.Context()),
	)
	if err != nil {
		s.writeDomainError(w, "delete config key", err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

// GetFeatureKey godoc
// @Summary Получить feature toggle
// @Description Возвращает состояние feature toggle по namespace и key.
// @Tags features
// @Produce json
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ feature toggle"
// @Success 200 {object} domain.FeatureToggle
// @Failure 404 {string} string "config item not found"
// @Failure 500 {string} string "internal server error"
// @Router /features/{namespace}/{key} [get]
func (s *Server) handleGetFeatureKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	item, err := s.featureService.GetKey(r.Context(), strings.TrimSpace(r.PathValue("namespace")), strings.TrimSpace(r.PathValue("key")))
	if err != nil {
		s.writeDomainError(w, "get feature key", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, item)
}

// PutFeatureKey godoc
// @Summary Создать или обновить feature toggle
// @Description Сохраняет feature toggle в Redis и публикует событие обновления для SDK.
// @Tags features
// @Accept json
// @Produce json
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ feature toggle"
// @Param request body PutFeatureKeyRequest true "Данные feature toggle"
// @Success 200 {object} domain.FeatureToggle
// @Failure 400 {string} string "bad request or validation error"
// @Failure 409 {string} string "version conflict"
// @Failure 500 {string} string "internal server error"
// @Router /features/{namespace}/{key} [put]
func (s *Server) handlePutFeatureKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req PutFeatureKeyRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	req.UpdatedBy = defaultUpdatedBy(req.UpdatedBy)

	item, err := s.featureService.Upsert(
		r.Context(),
		strings.TrimSpace(r.PathValue("namespace")),
		strings.TrimSpace(r.PathValue("key")),
		req.Enabled,
		req.ExpectedVersion,
		req.UpdatedBy,
		requestIDFromContext(r.Context()),
	)
	if err != nil {
		s.writeDomainError(w, "put feature key", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, item)
}

// DeleteFeatureKey godoc
// @Summary Удалить feature toggle
// @Description Удаляет feature toggle из Redis и публикует событие обновления.
// @Tags features
// @Produce json
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ feature toggle"
// @Param updatedBy query string false "Кто выполняет удаление"
// @Success 204
// @Failure 400 {string} string "validation error"
// @Failure 404 {string} string "config item not found"
// @Failure 500 {string} string "internal server error"
// @Router /features/{namespace}/{key} [delete]
func (s *Server) handleDeleteFeatureKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	updatedBy := defaultUpdatedBy(r.URL.Query().Get("updatedBy"))
	err := s.featureService.DeleteKey(
		r.Context(),
		strings.TrimSpace(r.PathValue("namespace")),
		strings.TrimSpace(r.PathValue("key")),
		updatedBy,
		requestIDFromContext(r.Context()),
	)
	if err != nil {
		s.writeDomainError(w, "delete feature key", err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

// UpdateConfig godoc
// @Summary Массовое обновление конфигурации
// @Description Обновляет несколько конфигурационных параметров в namespace и публикует событие обновления.
// @Tags legacy-config
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body domain.ConfigUpdateRequest true "Данные обновления конфигурации"
// @Success 200 {object} ConfigUpdateResponse
// @Failure 400 {string} string "bad request or validation error"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 409 {string} string "version conflict"
// @Failure 500 {string} string "internal server error"
// @Router /config/update [post]
func (s *Server) handleUpdate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req domain.ConfigUpdateRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	if err := s.ensureUpdatedByMatchesToken(r, &req.UpdatedBy); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}

	items, err := s.configService.Update(r.Context(), req, requestIDFromContext(r.Context()))
	if err != nil {
		var conflict *domain.VersionConflictError
		var validationErr *domain.ValidationError
		switch {
		case errors.As(err, &conflict):
			stdhttp.Error(w, err.Error(), stdhttp.StatusConflict)
		case errors.As(err, &validationErr):
			stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		default:
			s.internalError(w, "update config", err)
		}
		return
	}

	s.logger.Info("config updated",
		"namespace", req.Namespace,
		"keys", extractKeys(req.Entries),
		"dry_run", req.DryRun,
		"request_id", requestIDFromContext(r.Context()),
	)
	s.writeJSON(w, stdhttp.StatusOK, map[string]any{
		"namespace": req.Namespace,
		"dryRun":    req.DryRun,
		"items":     items,
	})
}

// FlushCache godoc
// @Summary Опубликовать flush-событие
// @Description Публикует событие принудительного перечитывания конфигурации для namespace.
// @Tags legacy-cache
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body domain.FlushRequest true "Данные flush-запроса"
// @Success 202 {object} FlushResponse
// @Failure 400 {string} string "bad request"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 500 {string} string "internal server error"
// @Router /cache/flush [post]
func (s *Server) handleFlush(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req domain.FlushRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	updatedBy := req.UpdatedBy
	if err := s.ensureUpdatedByMatchesToken(r, &updatedBy); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	req.UpdatedBy = updatedBy
	if err := s.configService.Flush(r.Context(), req, requestIDFromContext(r.Context())); err != nil {
		s.internalError(w, "flush namespace", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusAccepted, map[string]any{
		"namespace": req.Namespace,
		"status":    "flush published",
	})
}

// ImportConfig godoc
// @Summary Импорт конфигурации
// @Description Импортирует конфигурацию из JSON или YAML payload и публикует событие обновления.
// @Tags legacy-config
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body domain.ConfigImportRequest true "Данные импорта конфигурации"
// @Success 200 {object} ConfigUpdateResponse
// @Failure 400 {string} string "bad request or validation error"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 409 {string} string "version conflict"
// @Failure 500 {string} string "internal server error"
// @Router /config/import [post]
func (s *Server) handleImport(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		stdhttp.Error(w, "failed to read request body", stdhttp.StatusBadRequest)
		return
	}

	req, err := configservice.DecodeImportRequest(payload, r.Header.Get("Content-Type"))
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	if err := s.ensureUpdatedByMatchesToken(r, &req.UpdatedBy); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}

	items, err := s.configService.Import(r.Context(), req, requestIDFromContext(r.Context()))
	if err != nil {
		var conflict *domain.VersionConflictError
		var validationErr *domain.ValidationError
		switch {
		case errors.As(err, &conflict):
			stdhttp.Error(w, err.Error(), stdhttp.StatusConflict)
		case errors.As(err, &validationErr):
			stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		default:
			s.internalError(w, "import config", err)
		}
		return
	}

	s.writeJSON(w, stdhttp.StatusOK, map[string]any{
		"namespace": req.Namespace,
		"dryRun":    req.DryRun,
		"items":     items,
	})
}

// ExportConfig godoc
// @Summary Экспорт конфигурации
// @Description Экспортирует конфигурацию namespace. При format=yaml возвращает тот же набор данных в YAML.
// @Tags legacy-config
// @Produce json
// @Security BearerAuth
// @Param namespace query string true "Namespace сервиса"
// @Param format query string false "Формат ответа: json или yaml"
// @Success 200 {object} domain.ExportResponse
// @Failure 400 {string} string "namespace is required or unsupported format"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 500 {string} string "internal server error"
// @Router /config/export [get]
func (s *Server) handleExport(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		stdhttp.Error(w, "namespace is required", stdhttp.StatusBadRequest)
		return
	}
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = "json"
	}

	response, err := s.configService.Export(r.Context(), namespace)
	if err != nil {
		s.internalError(w, "export namespace", err)
		return
	}

	masked := response
	for i := range masked.Items {
		if masked.Items[i].IsSecret {
			masked.Items[i].Value = auditservice.MaskValue(masked.Items[i].Value, true)
		}
	}

	switch format {
	case "json":
		s.writeJSON(w, stdhttp.StatusOK, masked)
	case "yaml":
		payload, err := yaml.Marshal(masked)
		if err != nil {
			s.internalError(w, "marshal yaml", err)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = w.Write(payload)
	default:
		stdhttp.Error(w, "unsupported format", stdhttp.StatusBadRequest)
	}
}

// GetAudit godoc
// @Summary Получить аудит namespace
// @Description Возвращает историю изменений конфигурации для namespace.
// @Tags audit
// @Produce json
// @Security BearerAuth
// @Param namespace query string true "Namespace сервиса"
// @Success 200 {object} AuditResponse
// @Failure 400 {string} string "namespace is required"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 500 {string} string "internal server error"
// @Router /audit [get]
func (s *Server) handleAudit(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		stdhttp.Error(w, "namespace is required", stdhttp.StatusBadRequest)
		return
	}
	records, err := s.configService.GetAudit(r.Context(), namespace)
	if err != nil {
		s.internalError(w, "get audit", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusOK, map[string]any{
		"namespace": namespace,
		"items":     records,
	})
}

func (s *Server) ensureUpdatedByMatchesToken(r *stdhttp.Request, updatedBy *string) error {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return fmt.Errorf("missing auth context")
	}
	subject := strings.TrimSpace(claims.Subject)
	if subject == "" {
		return fmt.Errorf("token subject is empty")
	}
	if strings.TrimSpace(*updatedBy) == "" {
		*updatedBy = subject
		return nil
	}
	if *updatedBy != subject {
		return fmt.Errorf("updatedBy must match token subject")
	}
	return nil
}

func (s *Server) writeJSON(w stdhttp.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeDomainError(w stdhttp.ResponseWriter, operation string, err error) {
	var conflict *domain.VersionConflictError
	var validationErr *domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		stdhttp.Error(w, err.Error(), stdhttp.StatusNotFound)
	case errors.As(err, &conflict):
		stdhttp.Error(w, err.Error(), stdhttp.StatusConflict)
	case errors.As(err, &validationErr):
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
	default:
		s.internalError(w, operation, err)
	}
}

func (s *Server) internalError(w stdhttp.ResponseWriter, operation string, err error) {
	s.logger.Error(operation+" failed", "error", err)
	stdhttp.Error(w, "internal server error", stdhttp.StatusInternalServerError)
}

type requestIDKey struct{}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

func randomRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "req-fallback"
	}
	return hex.EncodeToString(buf)
}

func decodePayload(r *stdhttp.Request, out any) error {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "yaml"), strings.Contains(contentType, "yml"):
		return yaml.NewDecoder(r.Body).Decode(out)
	default:
		return json.NewDecoder(r.Body).Decode(out)
	}
}

func extractKeys(entries []domain.ConfigUpdateEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

func defaultUpdatedBy(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "api"
	}
	return value
}

type PutConfigKeyRequest struct {
	Value           string `json:"value" yaml:"value" example:"15"`
	Type            string `json:"type" yaml:"type" example:"int"`
	ExpectedVersion int64  `json:"expectedVersion" yaml:"expectedVersion" example:"0"`
	IsSecret        bool   `json:"isSecret" yaml:"isSecret" example:"false"`
	UpdatedBy       string `json:"updatedBy" yaml:"updatedBy" example:"admin@example.com"`
}

type PutFeatureKeyRequest struct {
	Enabled         bool   `json:"enabled" yaml:"enabled" example:"true"`
	ExpectedVersion int64  `json:"expectedVersion" yaml:"expectedVersion" example:"0"`
	UpdatedBy       string `json:"updatedBy" yaml:"updatedBy" example:"admin@example.com"`
}

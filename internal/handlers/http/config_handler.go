package http

import (
	"bytes"
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
	mux.Handle("GET /metrics", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleMetrics)))
	mux.Handle("/swagger/", httpSwagger.WrapHandler)
	mux.Handle("GET /configs", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleGetAllConfigs)))
	mux.Handle("GET /configs/{namespace}/{key}", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleGetConfigKey)))
	mux.Handle("PUT /configs/{namespace}/{key}", s.auth.Middleware(auth.RoleEditor, stdhttp.HandlerFunc(s.handlePutConfigKey)))
	mux.Handle("DELETE /configs/{namespace}/{key}", s.auth.Middleware(auth.RoleOwner, stdhttp.HandlerFunc(s.handleDeleteConfigKey)))
	mux.Handle("GET /features/{namespace}/{key}", s.auth.Middleware(auth.RoleReader, stdhttp.HandlerFunc(s.handleGetFeatureKey)))
	mux.Handle("PUT /features/{namespace}/{key}", s.auth.Middleware(auth.RoleEditor, stdhttp.HandlerFunc(s.handlePutFeatureKey)))
	mux.Handle("DELETE /features/{namespace}/{key}", s.auth.Middleware(auth.RoleOwner, stdhttp.HandlerFunc(s.handleDeleteFeatureKey)))
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
// @Security BearerAuth
// @Success 200 {string} string "Prometheus metrics"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
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

// GetAllConfigs godoc
// @Summary Получить все namespace и все config-значения
// @Description Возвращает все namespace, в которых есть config-значения, и полный набор config-элементов по каждому namespace.
// @Tags configs
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ConfigCatalogResponse
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 500 {string} string "internal server error"
// @Router /configs [get]
func (s *Server) handleGetAllConfigs(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	items, err := s.configService.GetAll(r.Context())
	if err != nil {
		s.internalError(w, "get all configs", err)
		return
	}

	namespaces := make([]string, 0, len(items))
	for _, item := range items {
		namespaces = append(namespaces, item.Namespace)
	}

	s.writeJSON(w, stdhttp.StatusOK, map[string]any{
		"namespaces": namespaces,
		"items":      items,
	})
}

// GetConfigKey godoc
// @Summary Получить конфигурационный параметр
// @Description Возвращает конфигурационный параметр по namespace и key.
// @Tags configs
// @Produce json
// @Security BearerAuth
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ параметра"
// @Success 200 {object} domain.ConfigItem
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
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
// @Description Upserts a single config item. The client does not send expectedVersion; the server takes a per-resource Redis lock, checks the namespace bulk lock, reads the current value atomically, creates version 1 for a new key, or stores current version + 1 for an existing key. Concurrent writes to the same key, or writes while a bulk update/import holds the namespace lock, return 423 Locked instead of 409 because the resource is temporarily locked rather than version-conflicted. Only the addressed key is changed; other keys in the namespace are untouched. A successful write publishes one Redis Pub/Sub update event for that key.
// @Summary Создать или обновить конфигурационный параметр
// @Description Сохраняет конфигурационный параметр в Redis и публикует событие обновления для SDK.
// @Tags configs
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ параметра"
// @Param request body PutConfigKeyRequest true "Данные конфигурационного параметра"
// @Success 200 {object} domain.ConfigItem
// @Failure 400 {string} string "bad request or validation error"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 423 {string} string "resource or namespace is locked by another write operation"
// @Failure 500 {string} string "internal server error"
// @Router /configs/{namespace}/{key} [put]
func (s *Server) handlePutConfigKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req PutConfigKeyRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}

	item, err := s.configService.UpsertKey(
		r.Context(),
		strings.TrimSpace(r.PathValue("namespace")),
		strings.TrimSpace(r.PathValue("key")),
		req.Value,
		req.Type,
		req.IsSecret,
		updatedBy,
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
// @Security BearerAuth
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ параметра"
// @Success 204
// @Failure 400 {string} string "validation error"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 404 {string} string "config item not found"
// @Failure 500 {string} string "internal server error"
// @Router /configs/{namespace}/{key} [delete]
func (s *Server) handleDeleteConfigKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.URL.Query().Has("updatedBy") {
		stdhttp.Error(w, "updatedBy is not accepted; it is taken from token subject", stdhttp.StatusBadRequest)
		return
	}
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	err = s.configService.DeleteKey(
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
// @Security BearerAuth
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ feature toggle"
// @Success 200 {object} domain.FeatureToggle
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
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
// @Description Upserts a single feature toggle. The client does not send expectedVersion; the server takes a per-resource Redis lock, reads the current value atomically, creates version 1 for a new toggle, or stores current version + 1 for an existing toggle. Concurrent writes to the same toggle return 423 Locked instead of 409 because the resource is temporarily locked rather than version-conflicted. Only the addressed toggle is changed; other toggles are untouched. A successful write publishes one Redis Pub/Sub update event for that key.
// @Summary Создать или обновить feature toggle
// @Description Сохраняет feature toggle в Redis и публикует событие обновления для SDK.
// @Tags features
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ feature toggle"
// @Param request body PutFeatureKeyRequest true "Данные feature toggle"
// @Success 200 {object} domain.FeatureToggle
// @Failure 400 {string} string "bad request or validation error"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 423 {string} string "resource is locked by another write operation"
// @Failure 500 {string} string "internal server error"
// @Router /features/{namespace}/{key} [put]
func (s *Server) handlePutFeatureKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req PutFeatureKeyRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}

	item, err := s.featureService.Upsert(
		r.Context(),
		strings.TrimSpace(r.PathValue("namespace")),
		strings.TrimSpace(r.PathValue("key")),
		req.Enabled,
		updatedBy,
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
// @Security BearerAuth
// @Param namespace path string true "Namespace сервиса"
// @Param key path string true "Ключ feature toggle"
// @Success 204
// @Failure 400 {string} string "validation error"
// @Failure 401 {string} string "missing bearer token"
// @Failure 403 {string} string "forbidden"
// @Failure 404 {string} string "config item not found"
// @Failure 500 {string} string "internal server error"
// @Router /features/{namespace}/{key} [delete]
func (s *Server) handleDeleteFeatureKey(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.URL.Query().Has("updatedBy") {
		stdhttp.Error(w, "updatedBy is not accepted; it is taken from token subject", stdhttp.StatusBadRequest)
		return
	}
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	err = s.featureService.DeleteKey(
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
// @Description Merge semantics: only entries from the request are updated or created, and config items omitted from the request are left unchanged. The client does not send expectedVersion; the server computes each next version. The namespace is protected by a Redis lock while the bulk operation runs. The request succeeds atomically for all entries or fails without partial writes. On success each changed item gets version+1 and the API publishes one Redis Pub/Sub event with the list of changed keys. When dryRun=true, the request validates and computes versions but does not persist data, create audit records, or publish events.
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
// @Failure 423 {string} string "namespace is locked by another write operation"
// @Failure 500 {string} string "internal server error"
// @Router /config/update [post]
func (s *Server) handleUpdate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req domain.ConfigUpdateRequest
	if err := decodePayload(r, &req); err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}

	items, err := s.configService.Update(r.Context(), req, updatedBy, requestIDFromContext(r.Context()))
	if err != nil {
		var locked *domain.NamespaceLockedError
		var validationErr *domain.ValidationError
		switch {
		case errors.As(err, &locked):
			stdhttp.Error(w, err.Error(), stdhttp.StatusLocked)
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
// @Description Does not modify stored config values. It only publishes a Redis Pub/Sub flush event for the namespace so SDK clients can perform a full namespace reload.
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
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	if err := s.configService.Flush(r.Context(), req, updatedBy, requestIDFromContext(r.Context())); err != nil {
		s.internalError(w, "flush namespace", err)
		return
	}
	s.writeJSON(w, stdhttp.StatusAccepted, map[string]any{
		"namespace": req.Namespace,
		"status":    "flush published",
	})
}

// ImportConfig godoc
// @Description Merge semantics: import converts the payload to the same update flow as /config/update. Only items present in the request are updated or created; existing config items omitted from the payload are left unchanged. The client does not send expectedVersion; the server computes each next version. The namespace is protected by a Redis lock while the import runs. The request is atomic for the whole payload and publishes one Redis Pub/Sub update event with all changed keys. When dryRun=true, nothing is persisted and no event is published.
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
// @Failure 423 {string} string "namespace is locked by another write operation"
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
	updatedBy, err := subjectFromToken(r)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}

	items, err := s.configService.Import(r.Context(), req, updatedBy, requestIDFromContext(r.Context()))
	if err != nil {
		var locked *domain.NamespaceLockedError
		var validationErr *domain.ValidationError
		switch {
		case errors.As(err, &locked):
			stdhttp.Error(w, err.Error(), stdhttp.StatusLocked)
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
// @Description Exports config items for one namespace as JSON or YAML. Secret items are masked as "****", so export output is useful for inspection but is not a lossless backup for secret values and cannot be imported back to restore secrets as-is.
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

func subjectFromToken(r *stdhttp.Request) (string, error) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return "", fmt.Errorf("missing auth context")
	}
	subject := strings.TrimSpace(claims.Subject)
	if subject == "" {
		return "", fmt.Errorf("token subject is empty")
	}
	return subject, nil
}

func (s *Server) writeJSON(w stdhttp.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeDomainError(w stdhttp.ResponseWriter, operation string, err error) {
	var locked *domain.NamespaceLockedError
	var resourceLocked *domain.ResourceLockedError
	var validationErr *domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		stdhttp.Error(w, err.Error(), stdhttp.StatusNotFound)
	case errors.As(err, &locked):
		stdhttp.Error(w, err.Error(), stdhttp.StatusLocked)
	case errors.As(err, &resourceLocked):
		stdhttp.Error(w, err.Error(), stdhttp.StatusLocked)
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
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	if containsTopLevelField(payload, r.Header.Get("Content-Type"), "updatedBy") {
		return fmt.Errorf("updatedBy is not accepted; it is taken from token subject")
	}
	return decodePayloadBytes(payload, r.Header.Get("Content-Type"), out)
}

func decodePayloadBytes(payload []byte, contentType string, out any) error {
	contentType = strings.ToLower(contentType)
	switch {
	case strings.Contains(contentType, "yaml"), strings.Contains(contentType, "yml"):
		return yaml.NewDecoder(bytes.NewReader(payload)).Decode(out)
	default:
		return json.NewDecoder(bytes.NewReader(payload)).Decode(out)
	}
}

func containsTopLevelField(payload []byte, contentType, key string) bool {
	var decoded any
	contentType = strings.ToLower(contentType)
	var err error
	if strings.Contains(contentType, "yaml") || strings.Contains(contentType, "yml") {
		err = yaml.NewDecoder(bytes.NewReader(payload)).Decode(&decoded)
	} else {
		err = json.NewDecoder(bytes.NewReader(payload)).Decode(&decoded)
	}
	if err != nil {
		return false
	}
	switch typed := decoded.(type) {
	case map[string]any:
		_, ok := typed[key]
		return ok
	case map[any]any:
		_, ok := typed[key]
		return ok
	}
	return false
}

func extractKeys(entries []domain.ConfigUpdateEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

type PutConfigKeyRequest struct {
	Value    string `json:"value" yaml:"value" example:"15"`
	Type     string `json:"type" yaml:"type" example:"int"`
	IsSecret bool   `json:"isSecret" yaml:"isSecret" example:"false"`
}

type PutFeatureKeyRequest struct {
	Enabled bool `json:"enabled" yaml:"enabled" example:"true"`
}

package api

import (
	"context"
	"embed"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/audit"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/auth"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/metrics"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/model"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/repository"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/service"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/validation"
)

//go:embed openapi.yaml
var openAPISpec embed.FS

type Server struct {
	logger   *slog.Logger
	service  *service.ConfigService
	auth     *auth.Manager
	metrics  *metrics.Registry
	httpAddr string
}

func NewServer(httpAddr string, logger *slog.Logger, service *service.ConfigService, authManager *auth.Manager, metricsRegistry *metrics.Registry) *Server {
	return &Server{
		logger:   logger,
		service:  service,
		auth:     authManager,
		metrics:  metricsRegistry,
		httpAddr: httpAddr,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /docs", s.handleDocs)
	mux.HandleFunc("GET /docs/", s.handleDocs)
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPISpec)
	mux.Handle("GET /metrics", s.metrics.Handler())
	mux.Handle("GET /config", s.auth.Middleware(auth.RoleReader, http.HandlerFunc(s.handleGetConfig)))
	mux.Handle("POST /config/update", s.auth.Middleware(auth.RoleEditor, http.HandlerFunc(s.handleUpdate)))
	mux.Handle("POST /cache/flush", s.auth.Middleware(auth.RoleOwner, http.HandlerFunc(s.handleFlush)))
	mux.Handle("POST /config/import", s.auth.Middleware(auth.RoleOwner, http.HandlerFunc(s.handleImport)))
	mux.Handle("GET /config/export", s.auth.Middleware(auth.RoleReader, http.HandlerFunc(s.handleExport)))
	mux.Handle("GET /audit", s.auth.Middleware(auth.RoleReader, http.HandlerFunc(s.handleAudit)))
	return s.withRequestContext(mux)
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("starting remote-config-api", "addr", s.httpAddr)
	return http.ListenAndServe(s.httpAddr, s.Handler())
}

func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = randomRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Health(r.Context()); err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "degraded",
			"redis":  "down",
			"error":  err.Error(),
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"redis":  "up",
	})
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerUIHTML))
}

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	spec, err := openAPISpec.ReadFile("openapi.yaml")
	if err != nil {
		s.internalError(w, "read openapi spec", err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(spec)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		http.Error(w, "namespace is required", http.StatusBadRequest)
		return
	}
	items, err := s.service.GetNamespace(r.Context(), namespace)
	if err != nil {
		s.internalError(w, "get namespace", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"namespace": namespace,
		"items":     items,
	})
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req model.ConfigUpdateRequest
	if err := decodePayload(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.ensureUpdatedByMatchesToken(r, &req.UpdatedBy); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	items, err := s.service.Update(r.Context(), req, requestIDFromContext(r.Context()))
	if err != nil {
		var conflict *repository.VersionConflictError
		var validationErr *validation.Error
		switch {
		case errors.As(err, &conflict):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.As(err, &validationErr):
			http.Error(w, err.Error(), http.StatusBadRequest)
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
	s.writeJSON(w, http.StatusOK, map[string]any{
		"namespace": req.Namespace,
		"dryRun":    req.DryRun,
		"items":     items,
	})
}

func (s *Server) handleFlush(w http.ResponseWriter, r *http.Request) {
	var req model.FlushRequest
	if err := decodePayload(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updatedBy := req.UpdatedBy
	if err := s.ensureUpdatedByMatchesToken(r, &updatedBy); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.UpdatedBy = updatedBy
	if err := s.service.Flush(r.Context(), req, requestIDFromContext(r.Context())); err != nil {
		s.internalError(w, "flush namespace", err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"namespace": req.Namespace,
		"status":    "flush published",
	})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	req, err := validation.DecodeImportRequest(payload, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.ensureUpdatedByMatchesToken(r, &req.UpdatedBy); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.service.Import(r.Context(), req, requestIDFromContext(r.Context()))
	if err != nil {
		var conflict *repository.VersionConflictError
		var validationErr *validation.Error
		switch {
		case errors.As(err, &conflict):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.As(err, &validationErr):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			s.internalError(w, "import config", err)
		}
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"namespace": req.Namespace,
		"dryRun":    req.DryRun,
		"items":     items,
	})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		http.Error(w, "namespace is required", http.StatusBadRequest)
		return
	}
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = "json"
	}

	response, err := s.service.Export(r.Context(), namespace)
	if err != nil {
		s.internalError(w, "export namespace", err)
		return
	}

	masked := response
	for i := range masked.Items {
		if masked.Items[i].IsSecret {
			masked.Items[i].Value = audit.MaskValue(masked.Items[i].Value, true)
		}
	}

	switch format {
	case "json":
		s.writeJSON(w, http.StatusOK, masked)
	case "yaml":
		payload, err := yaml.Marshal(masked)
		if err != nil {
			s.internalError(w, "marshal yaml", err)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		http.Error(w, "namespace is required", http.StatusBadRequest)
		return
	}
	records, err := s.service.GetAudit(r.Context(), namespace)
	if err != nil {
		s.internalError(w, "get audit", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"namespace": namespace,
		"items":     records,
	})
}

func (s *Server) ensureUpdatedByMatchesToken(r *http.Request, updatedBy *string) error {
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

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) internalError(w http.ResponseWriter, operation string, err error) {
	s.logger.Error(operation+" failed", "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
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

func decodePayload(r *http.Request, out any) error {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "yaml"), strings.Contains(contentType, "yml"):
		return yaml.NewDecoder(r.Body).Decode(out)
	default:
		return json.NewDecoder(r.Body).Decode(out)
	}
}

func extractKeys(entries []model.ConfigUpdateEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

const swaggerUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Remote Config API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #0f172a; }
    .topbar { display: none; }
    #swagger-ui { box-sizing: border-box; padding: 16px; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '/openapi.yaml',
      dom_id: '#swagger-ui',
      deepLinking: true,
      persistAuthorization: true,
      layout: 'BaseLayout'
    });
  </script>
</body>
</html>`

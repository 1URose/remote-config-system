package service

import (
	"context"
	"fmt"
	"time"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/metrics"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/model"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/repository"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/validation"
)

type Repository interface {
	Ping(ctx context.Context) error
	GetNamespace(ctx context.Context, namespace string) ([]model.ConfigItem, error)
	GetKey(ctx context.Context, namespace, key string) (model.ConfigItem, error)
	GetKeys(ctx context.Context, namespace string, keys []string) ([]model.ConfigItem, error)
	Update(ctx context.Context, req model.ConfigUpdateRequest, requestID string) ([]model.ConfigItem, error)
	PublishFlush(ctx context.Context, namespace, updatedBy, requestID string) error
	GetAudit(ctx context.Context, namespace string) ([]model.AuditRecord, error)
}

type ConfigService struct {
	repo    Repository
	metrics *metrics.Registry
}

func NewConfigService(repo Repository, metrics *metrics.Registry) *ConfigService {
	return &ConfigService{repo: repo, metrics: metrics}
}

func (s *ConfigService) Health(ctx context.Context) error {
	err := s.repo.Ping(ctx)
	s.metrics.SetRedisConnected(err == nil)
	return err
}

func (s *ConfigService) GetNamespace(ctx context.Context, namespace string) ([]model.ConfigItem, error) {
	return s.repo.GetNamespace(ctx, namespace)
}

func (s *ConfigService) Update(ctx context.Context, req model.ConfigUpdateRequest, requestID string) ([]model.ConfigItem, error) {
	if err := validation.ValidateUpdateRequest(req); err != nil {
		return nil, err
	}

	start := time.Now()
	items, err := s.repo.Update(ctx, req, requestID)
	s.metrics.ObserveUpdateDuration(time.Since(start))

	if err != nil {
		s.metrics.IncFailedUpdates()
		if _, ok := err.(*repository.VersionConflictError); ok {
			s.metrics.IncVersionConflicts()
		}
		return nil, err
	}

	s.metrics.IncSuccessfulUpdates()
	return items, nil
}

func (s *ConfigService) Import(ctx context.Context, req model.ConfigUpdateRequest, requestID string) ([]model.ConfigItem, error) {
	return s.Update(ctx, req, requestID)
}

func (s *ConfigService) Flush(ctx context.Context, req model.FlushRequest, requestID string) error {
	if req.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if req.UpdatedBy == "" {
		return fmt.Errorf("updatedBy is required")
	}
	s.metrics.IncFlushRequests()
	s.metrics.IncReloadRequests()
	return s.repo.PublishFlush(ctx, req.Namespace, req.UpdatedBy, requestID)
}

func (s *ConfigService) Export(ctx context.Context, namespace string) (model.ExportResponse, error) {
	items, err := s.repo.GetNamespace(ctx, namespace)
	if err != nil {
		return model.ExportResponse{}, err
	}
	return model.ExportResponse{
		Namespace: namespace,
		Items:     items,
	}, nil
}

func (s *ConfigService) GetAudit(ctx context.Context, namespace string) ([]model.AuditRecord, error) {
	return s.repo.GetAudit(ctx, namespace)
}

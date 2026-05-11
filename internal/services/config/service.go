package config

import (
	"context"
	"fmt"
	"time"

	"github.com/1URose/remote-config-system/internal/domain"
	"github.com/1URose/remote-config-system/internal/metrics"
)

type Storage interface {
	Ping(ctx context.Context) error
	ListNamespaces(ctx context.Context) ([]string, error)
	GetNamespace(ctx context.Context, namespace string) ([]domain.ConfigItem, error)
	GetKey(ctx context.Context, namespace, key string) (domain.ConfigItem, error)
	GetKeys(ctx context.Context, namespace string, keys []string) ([]domain.ConfigItem, error)
	Update(ctx context.Context, req domain.ConfigUpdateRequest, requestID string) ([]domain.ConfigItem, error)
	DeleteKey(ctx context.Context, namespace, key, updatedBy, requestID string) error
	GetAudit(ctx context.Context, namespace string) ([]domain.AuditRecord, error)
}

type Publisher interface {
	PublishFlush(ctx context.Context, namespace, updatedBy, requestID string) error
}

type Service struct {
	storage   Storage
	publisher Publisher
	metrics   *metrics.Registry
}

func NewService(storage Storage, publisher Publisher, metrics *metrics.Registry) *Service {
	return &Service{
		storage:   storage,
		publisher: publisher,
		metrics:   metrics,
	}
}

func (s *Service) Health(ctx context.Context) error {
	err := s.storage.Ping(ctx)
	s.metrics.SetRedisConnected(err == nil)
	return err
}

func (s *Service) GetNamespace(ctx context.Context, namespace string) ([]domain.ConfigItem, error) {
	return s.storage.GetNamespace(ctx, namespace)
}

func (s *Service) GetAll(ctx context.Context) ([]domain.ExportResponse, error) {
	namespaces, err := s.storage.ListNamespaces(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]domain.ExportResponse, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespaceItems, err := s.storage.GetNamespace(ctx, namespace)
		if err != nil {
			return nil, err
		}
		items = append(items, domain.ExportResponse{
			Namespace: namespace,
			Items:     namespaceItems,
		})
	}
	return items, nil
}

func (s *Service) GetKey(ctx context.Context, namespace, key string) (domain.ConfigItem, error) {
	return s.storage.GetKey(ctx, namespace, key)
}

func (s *Service) Update(ctx context.Context, req domain.ConfigUpdateRequest, requestID string) ([]domain.ConfigItem, error) {
	if err := ValidateUpdateRequest(req); err != nil {
		return nil, err
	}

	start := time.Now()
	items, err := s.storage.Update(ctx, req, requestID)
	s.metrics.ObserveUpdateDuration(time.Since(start))

	if err != nil {
		s.metrics.IncFailedUpdates()
		if _, ok := err.(*domain.VersionConflictError); ok {
			s.metrics.IncVersionConflicts()
		}
		return nil, err
	}

	s.metrics.IncSuccessfulUpdates()
	return items, nil
}

func (s *Service) UpsertKey(ctx context.Context, namespace, key, value, kind string, expectedVersion int64, isSecret bool, updatedBy, requestID string) (domain.ConfigItem, error) {
	items, err := s.Update(ctx, domain.ConfigUpdateRequest{
		Namespace: namespace,
		UpdatedBy: updatedBy,
		Entries: []domain.ConfigUpdateEntry{
			{
				Key:             key,
				Value:           value,
				Type:            kind,
				ExpectedVersion: expectedVersion,
				IsSecret:        isSecret,
			},
		},
	}, requestID)
	if err != nil {
		return domain.ConfigItem{}, err
	}
	if len(items) != 1 {
		return domain.ConfigItem{}, fmt.Errorf("expected single updated item, got %d", len(items))
	}
	return items[0], nil
}

func (s *Service) DeleteKey(ctx context.Context, namespace, key, updatedBy, requestID string) error {
	if namespace == "" {
		return newValidationError("namespace is required")
	}
	if key == "" {
		return newValidationError("key is required")
	}
	if updatedBy == "" {
		return newValidationError("updatedBy is required")
	}
	return s.storage.DeleteKey(ctx, namespace, key, updatedBy, requestID)
}

func (s *Service) Import(ctx context.Context, req domain.ConfigUpdateRequest, requestID string) ([]domain.ConfigItem, error) {
	return s.Update(ctx, req, requestID)
}

func (s *Service) Flush(ctx context.Context, req domain.FlushRequest, requestID string) error {
	if req.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if req.UpdatedBy == "" {
		return fmt.Errorf("updatedBy is required")
	}
	s.metrics.IncFlushRequests()
	s.metrics.IncReloadRequests()
	return s.publisher.PublishFlush(ctx, req.Namespace, req.UpdatedBy, requestID)
}

func (s *Service) Export(ctx context.Context, namespace string) (domain.ExportResponse, error) {
	items, err := s.storage.GetNamespace(ctx, namespace)
	if err != nil {
		return domain.ExportResponse{}, err
	}
	return domain.ExportResponse{
		Namespace: namespace,
		Items:     items,
	}, nil
}

func (s *Service) GetAudit(ctx context.Context, namespace string) ([]domain.AuditRecord, error) {
	return s.storage.GetAudit(ctx, namespace)
}

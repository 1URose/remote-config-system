package feature

import (
	"context"
	"strings"

	"github.com/1URose/remote-config-system/internal/domain"
)

type Storage interface {
	GetNamespace(ctx context.Context, namespace string) ([]domain.FeatureToggle, error)
	GetKey(ctx context.Context, namespace, key string) (domain.FeatureToggle, error)
	GetKeys(ctx context.Context, namespace string, keys []string) ([]domain.FeatureToggle, error)
	Upsert(ctx context.Context, namespace, key string, enabled bool, expectedVersion int64, updatedBy, requestID string) (domain.FeatureToggle, error)
	DeleteKey(ctx context.Context, namespace, key, updatedBy, requestID string) error
}

type Service struct {
	storage Storage
}

func NewService(storage Storage) *Service {
	return &Service{storage: storage}
}

func (s *Service) GetKey(ctx context.Context, namespace, key string) (domain.FeatureToggle, error) {
	return s.storage.GetKey(ctx, namespace, key)
}

func (s *Service) GetNamespace(ctx context.Context, namespace string) ([]domain.FeatureToggle, error) {
	return s.storage.GetNamespace(ctx, namespace)
}

func (s *Service) Upsert(ctx context.Context, namespace, key string, enabled bool, expectedVersion int64, updatedBy, requestID string) (domain.FeatureToggle, error) {
	if strings.TrimSpace(namespace) == "" {
		return domain.FeatureToggle{}, domain.NewValidationError("namespace is required")
	}
	if strings.TrimSpace(key) == "" {
		return domain.FeatureToggle{}, domain.NewValidationError("key is required")
	}
	if strings.TrimSpace(updatedBy) == "" {
		return domain.FeatureToggle{}, domain.NewValidationError("updatedBy is required")
	}
	if expectedVersion < 0 {
		return domain.FeatureToggle{}, domain.NewValidationError("expectedVersion must be >= 0")
	}
	return s.storage.Upsert(ctx, namespace, key, enabled, expectedVersion, updatedBy, requestID)
}

func (s *Service) DeleteKey(ctx context.Context, namespace, key, updatedBy, requestID string) error {
	if strings.TrimSpace(namespace) == "" {
		return domain.NewValidationError("namespace is required")
	}
	if strings.TrimSpace(key) == "" {
		return domain.NewValidationError("key is required")
	}
	if strings.TrimSpace(updatedBy) == "" {
		return domain.NewValidationError("updatedBy is required")
	}
	return s.storage.DeleteKey(ctx, namespace, key, updatedBy, requestID)
}

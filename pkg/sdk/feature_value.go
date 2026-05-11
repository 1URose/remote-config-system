package sdk

import (
	"time"

	"github.com/1URose/remote-config-system/internal/domain"
)

type Feature struct {
	Namespace string
	Key       string
	Enabled   bool
	Version   int64
	UpdatedAt time.Time
	UpdatedBy string
}

func newFeature(item domain.FeatureToggle) Feature {
	return Feature{
		Namespace: item.Namespace,
		Key:       item.Key,
		Enabled:   item.Enabled,
		Version:   item.Version,
		UpdatedAt: item.UpdatedAt,
		UpdatedBy: item.UpdatedBy,
	}
}

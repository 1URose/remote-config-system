package domain

import "time"

type FeatureToggle struct {
	Namespace string    `json:"namespace" yaml:"namespace"`
	Key       string    `json:"key" yaml:"key"`
	Enabled   bool      `json:"enabled" yaml:"enabled"`
	Version   int64     `json:"version" yaml:"version"`
	UpdatedAt time.Time `json:"updatedAt" yaml:"updatedAt"`
	UpdatedBy string    `json:"updatedBy" yaml:"updatedBy"`
}

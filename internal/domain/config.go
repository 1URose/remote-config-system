package domain

import "time"

type ConfigItem struct {
	Namespace string    `json:"namespace" yaml:"namespace" example:"demo-service"`
	Key       string    `json:"key" yaml:"key" example:"discount.percent"`
	Value     string    `json:"value" yaml:"value" example:"25"`
	Type      string    `json:"type" yaml:"type" example:"int"`
	Version   int64     `json:"version" yaml:"version" example:"1"`
	IsSecret  bool      `json:"isSecret" yaml:"isSecret" example:"false"`
	UpdatedAt time.Time `json:"updatedAt" yaml:"updatedAt"`
	UpdatedBy string    `json:"updatedBy" yaml:"updatedBy" example:"admin@example.com"`
}

type ConfigUpdateRequest struct {
	Namespace string              `json:"namespace" yaml:"namespace" example:"demo-service"`
	DryRun    bool                `json:"dryRun" yaml:"dryRun" example:"false"`
	Entries   []ConfigUpdateEntry `json:"entries" yaml:"entries"`
}

type ConfigImportRequest struct {
	Namespace string                      `json:"namespace" yaml:"namespace" example:"demo-service"`
	DryRun    bool                        `json:"dryRun" yaml:"dryRun" example:"false"`
	Entries   []ConfigUpdateEntry         `json:"entries,omitempty" yaml:"entries,omitempty"`
	Items     map[string]ConfigImportItem `json:"items,omitempty" yaml:"items,omitempty"`
}

type ConfigImportItem struct {
	Value    any    `json:"value" yaml:"value"`
	Type     string `json:"type,omitempty" yaml:"type,omitempty" example:"int"`
	IsSecret bool   `json:"isSecret,omitempty" yaml:"isSecret,omitempty" example:"false"`
}

type ConfigUpdateEntry struct {
	Key      string `json:"key" yaml:"key" example:"discount.percent"`
	Value    string `json:"value" yaml:"value" example:"25"`
	Type     string `json:"type" yaml:"type" example:"int"`
	IsSecret bool   `json:"isSecret" yaml:"isSecret" example:"false"`
}

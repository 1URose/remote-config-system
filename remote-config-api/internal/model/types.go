package model

import "time"

type ConfigItem struct {
	Namespace string    `json:"namespace" yaml:"namespace"`
	Key       string    `json:"key" yaml:"key"`
	Value     string    `json:"value" yaml:"value"`
	Type      string    `json:"type" yaml:"type"`
	Version   int64     `json:"version" yaml:"version"`
	IsSecret  bool      `json:"isSecret" yaml:"isSecret"`
	UpdatedAt time.Time `json:"updatedAt" yaml:"updatedAt"`
	UpdatedBy string    `json:"updatedBy" yaml:"updatedBy"`
}

type ConfigUpdateRequest struct {
	Namespace string              `json:"namespace" yaml:"namespace"`
	UpdatedBy string              `json:"updatedBy" yaml:"updatedBy"`
	DryRun    bool                `json:"dryRun" yaml:"dryRun"`
	Entries   []ConfigUpdateEntry `json:"entries" yaml:"entries"`
}

type ConfigImportRequest struct {
	Namespace string                    `json:"namespace" yaml:"namespace"`
	UpdatedBy string                    `json:"updatedBy" yaml:"updatedBy"`
	DryRun    bool                      `json:"dryRun" yaml:"dryRun"`
	Entries   []ConfigUpdateEntry       `json:"entries,omitempty" yaml:"entries,omitempty"`
	Items     map[string]ConfigImportItem `json:"items,omitempty" yaml:"items,omitempty"`
}

type ConfigImportItem struct {
	Value           any    `json:"value" yaml:"value"`
	Type            string `json:"type,omitempty" yaml:"type,omitempty"`
	ExpectedVersion *int64 `json:"expectedVersion,omitempty" yaml:"expectedVersion,omitempty"`
	IsSecret        bool   `json:"isSecret,omitempty" yaml:"isSecret,omitempty"`
}

type ConfigUpdateEntry struct {
	Key             string `json:"key" yaml:"key"`
	Value           string `json:"value" yaml:"value"`
	Type            string `json:"type" yaml:"type"`
	ExpectedVersion int64  `json:"expectedVersion" yaml:"expectedVersion"`
	IsSecret        bool   `json:"isSecret" yaml:"isSecret"`
}

type ConfigUpdateEvent struct {
	Namespace string    `json:"namespace" yaml:"namespace"`
	Keys      []string  `json:"keys" yaml:"keys"`
	Operation string    `json:"operation" yaml:"operation"`
	UpdatedAt time.Time `json:"updatedAt" yaml:"updatedAt"`
	UpdatedBy string    `json:"updatedBy" yaml:"updatedBy"`
	RequestID string    `json:"requestId" yaml:"requestId"`
}

type AuditRecord struct {
	Namespace string    `json:"namespace" yaml:"namespace"`
	Key       string    `json:"key" yaml:"key"`
	OldValue  string    `json:"oldValue" yaml:"oldValue"`
	NewValue  string    `json:"newValue" yaml:"newValue"`
	Type      string    `json:"type" yaml:"type"`
	Version   int64     `json:"version" yaml:"version"`
	IsSecret  bool      `json:"isSecret" yaml:"isSecret"`
	UpdatedAt time.Time `json:"updatedAt" yaml:"updatedAt"`
	UpdatedBy string    `json:"updatedBy" yaml:"updatedBy"`
	RequestID string    `json:"requestId" yaml:"requestId"`
	Result    string    `json:"result" yaml:"result"`
}

type FlushRequest struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	UpdatedBy string `json:"updatedBy" yaml:"updatedBy"`
}

type ExportResponse struct {
	Namespace string       `json:"namespace" yaml:"namespace"`
	Items     []ConfigItem `json:"items" yaml:"items"`
}

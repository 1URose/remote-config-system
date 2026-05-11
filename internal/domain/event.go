package domain

import "time"

type ConfigUpdateEvent struct {
	Namespace string    `json:"namespace" yaml:"namespace"`
	Resource  string    `json:"resource,omitempty" yaml:"resource,omitempty"`
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

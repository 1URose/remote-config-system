package http

import "github.com/1URose/remote-config-system/internal/domain"

type HealthResponse struct {
	Status string `json:"status" example:"ok"`
	Redis  string `json:"redis" example:"up"`
	Error  string `json:"error,omitempty" example:"dial tcp 127.0.0.1:6379: connect: connection refused"`
}

type ConfigNamespaceResponse struct {
	Namespace string              `json:"namespace" example:"payments"`
	Items     []domain.ConfigItem `json:"items"`
}

type ConfigCatalogResponse struct {
	Namespaces []string                `json:"namespaces" example:"payments,demo-service"`
	Items      []domain.ExportResponse `json:"items"`
}

type ConfigUpdateResponse struct {
	Namespace string              `json:"namespace" example:"payments"`
	DryRun    bool                `json:"dryRun" example:"false"`
	Items     []domain.ConfigItem `json:"items"`
}

type FlushResponse struct {
	Namespace string `json:"namespace" example:"payments"`
	Status    string `json:"status" example:"flush published"`
}

type AuditResponse struct {
	Namespace string               `json:"namespace" example:"payments"`
	Items     []domain.AuditRecord `json:"items"`
}

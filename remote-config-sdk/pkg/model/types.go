package model

import "time"

type ConfigItem struct {
	Namespace string    `json:"namespace"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Type      string    `json:"type"`
	Version   int64     `json:"version"`
	IsSecret  bool      `json:"isSecret"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

type ConfigUpdateEvent struct {
	Namespace string    `json:"namespace"`
	Keys      []string  `json:"keys"`
	Operation string    `json:"operation"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
	RequestID string    `json:"requestId"`
}


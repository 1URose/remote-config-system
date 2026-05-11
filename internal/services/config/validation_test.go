package config

import (
	"testing"

	"github.com/1URose/remote-config-system/internal/domain"
)

func TestValidateValue(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		value   string
		wantErr bool
	}{
		{name: "string", kind: "string", value: "hello"},
		{name: "bool ok", kind: "bool", value: "true"},
		{name: "bool bad", kind: "bool", value: "nope", wantErr: true},
		{name: "int ok", kind: "int", value: "42"},
		{name: "int bad", kind: "int", value: "4.2", wantErr: true},
		{name: "float ok", kind: "float", value: "4.2"},
		{name: "json ok", kind: "json", value: `{"feature":true}`},
		{name: "json bad", kind: "json", value: `{`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateValue(tc.kind, tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateUpdateRequest(t *testing.T) {
	req := domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "flag", Value: "true", Type: "bool", ExpectedVersion: 0},
		},
	}
	if err := ValidateUpdateRequest(req); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestDecodeImportRequestFromYAMLItems(t *testing.T) {
	payload := []byte(`
namespace: payments
updatedBy: owner@example.com
dryRun: true
items:
  feature_x_enabled: true
  max_retries:
    value: 3
    type: int
    expectedVersion: 2
  config_blob:
    hello: world
`)

	req, err := DecodeImportRequest(payload, "application/x-yaml")
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}

	if req.Namespace != "payments" || req.UpdatedBy != "owner@example.com" || !req.DryRun {
		t.Fatalf("unexpected import request header: %+v", req)
	}
	if len(req.Entries) != 3 {
		t.Fatalf("expected 3 import entries, got %d", len(req.Entries))
	}
	if req.Entries[0].Key != "config_blob" || req.Entries[0].Type != "json" {
		t.Fatalf("expected sorted json entry for config_blob, got %+v", req.Entries[0])
	}
	if req.Entries[1].Key != "feature_x_enabled" || req.Entries[1].Type != "bool" || req.Entries[1].Value != "true" {
		t.Fatalf("unexpected feature_x_enabled entry: %+v", req.Entries[1])
	}
	if req.Entries[2].ExpectedVersion != 2 || req.Entries[2].Value != "3" {
		t.Fatalf("unexpected max_retries entry: %+v", req.Entries[2])
	}
}

func TestDecodeImportRequestRejectsMetadataWithoutValue(t *testing.T) {
	payload := []byte(`{"namespace":"payments","items":{"flag":{"type":"bool"}}}`)
	if _, err := DecodeImportRequest(payload, "application/json"); err == nil {
		t.Fatalf("expected error for metadata object without value")
	}
}

package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/1URose/remote-config-system/internal/domain"
)

var supportedTypes = map[string]struct{}{
	"string": {},
	"bool":   {},
	"int":    {},
	"float":  {},
	"json":   {},
}

func ValidateUpdateRequest(req domain.ConfigUpdateRequest) error {
	if strings.TrimSpace(req.Namespace) == "" {
		return newValidationError("namespace is required")
	}
	if strings.TrimSpace(req.UpdatedBy) == "" {
		return newValidationError("updatedBy is required")
	}
	if len(req.Entries) == 0 {
		return newValidationError("entries must not be empty")
	}

	seen := make(map[string]struct{}, len(req.Entries))
	for _, entry := range req.Entries {
		if strings.TrimSpace(entry.Key) == "" {
			return newValidationError("entry key is required")
		}
		if _, ok := seen[entry.Key]; ok {
			return newValidationError(fmt.Sprintf("duplicate key %q", entry.Key))
		}
		seen[entry.Key] = struct{}{}
		if err := ValidateEntry(entry); err != nil {
			return newValidationError(fmt.Sprintf("key %q: %v", entry.Key, err))
		}
	}

	return nil
}

func ValidateEntry(entry domain.ConfigUpdateEntry) error {
	if _, ok := supportedTypes[entry.Type]; !ok {
		return newValidationError(fmt.Sprintf("unsupported type %q", entry.Type))
	}
	return ValidateValue(entry.Type, entry.Value)
}

func ValidateValue(kind, value string) error {
	switch kind {
	case "string":
		return nil
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return newValidationError(fmt.Sprintf("invalid bool value: %v", err))
		}
	case "int":
		if _, err := strconv.Atoi(value); err != nil {
			return newValidationError(fmt.Sprintf("invalid int value: %v", err))
		}
	case "float":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return newValidationError(fmt.Sprintf("invalid float value: %v", err))
		}
	case "json":
		var payload any
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return newValidationError(fmt.Sprintf("invalid json value: %v", err))
		}
	default:
		return newValidationError(fmt.Sprintf("unsupported type %q", kind))
	}
	return nil
}

func newValidationError(message string) error {
	return domain.NewValidationError(message)
}

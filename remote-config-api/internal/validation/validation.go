package validation

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/model"
)

type Error struct {
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

var supportedTypes = map[string]struct{}{
	"string": {},
	"bool":   {},
	"int":    {},
	"float":  {},
	"json":   {},
}

func ValidateUpdateRequest(req model.ConfigUpdateRequest) error {
	if strings.TrimSpace(req.Namespace) == "" {
		return newError("namespace is required")
	}
	if strings.TrimSpace(req.UpdatedBy) == "" {
		return newError("updatedBy is required")
	}
	if len(req.Entries) == 0 {
		return newError("entries must not be empty")
	}

	seen := make(map[string]struct{}, len(req.Entries))
	for _, entry := range req.Entries {
		if strings.TrimSpace(entry.Key) == "" {
			return newError("entry key is required")
		}
		if _, ok := seen[entry.Key]; ok {
			return newError(fmt.Sprintf("duplicate key %q", entry.Key))
		}
		seen[entry.Key] = struct{}{}
		if err := ValidateEntry(entry); err != nil {
			return newError(fmt.Sprintf("key %q: %v", entry.Key, err))
		}
	}

	return nil
}

func ValidateEntry(entry model.ConfigUpdateEntry) error {
	if _, ok := supportedTypes[entry.Type]; !ok {
		return newError(fmt.Sprintf("unsupported type %q", entry.Type))
	}
	if entry.ExpectedVersion < 0 {
		return newError("expectedVersion must be >= 0")
	}
	return ValidateValue(entry.Type, entry.Value)
}

func ValidateValue(kind, value string) error {
	switch kind {
	case "string":
		return nil
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return newError(fmt.Sprintf("invalid bool value: %v", err))
		}
	case "int":
		if _, err := strconv.Atoi(value); err != nil {
			return newError(fmt.Sprintf("invalid int value: %v", err))
		}
	case "float":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return newError(fmt.Sprintf("invalid float value: %v", err))
		}
	case "json":
		var payload any
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return newError(fmt.Sprintf("invalid json value: %v", err))
		}
	default:
		return newError(fmt.Sprintf("unsupported type %q", kind))
	}
	return nil
}

func newError(message string) error {
	return &Error{Message: message}
}

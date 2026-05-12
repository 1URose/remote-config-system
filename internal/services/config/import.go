package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/1URose/remote-config-system/internal/domain"
)

func DecodeImportRequest(payload []byte, contentType string) (domain.ConfigUpdateRequest, error) {
	raw := make(map[string]any)
	if err := decodeFlexiblePayload(payload, contentType, &raw); err != nil {
		return domain.ConfigUpdateRequest{}, newValidationError(fmt.Sprintf("invalid import payload: %v", err))
	}
	raw = normalizeMap(raw).(map[string]any)

	req := domain.ConfigUpdateRequest{
		Namespace: asString(raw["namespace"]),
		UpdatedBy: asString(raw["updatedBy"]),
		DryRun:    asBool(raw["dryRun"]),
	}

	if entriesRaw, ok := raw["entries"]; ok && entriesRaw != nil {
		entries, err := parseEntries(entriesRaw)
		if err != nil {
			return domain.ConfigUpdateRequest{}, err
		}
		req.Entries = entries
		return req, nil
	}

	itemsRaw, ok := raw["items"]
	if !ok || itemsRaw == nil {
		return domain.ConfigUpdateRequest{}, newValidationError("import payload must contain either entries or items")
	}

	itemsMap, ok := normalizeMap(itemsRaw).(map[string]any)
	if !ok {
		return domain.ConfigUpdateRequest{}, newValidationError("items must be an object")
	}
	if len(itemsMap) == 0 {
		return domain.ConfigUpdateRequest{}, newValidationError("items must not be empty")
	}

	keys := make([]string, 0, len(itemsMap))
	for key := range itemsMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	req.Entries = make([]domain.ConfigUpdateEntry, 0, len(keys))
	for _, key := range keys {
		entry, err := parseImportItem(key, itemsMap[key])
		if err != nil {
			return domain.ConfigUpdateRequest{}, err
		}
		req.Entries = append(req.Entries, entry)
	}

	return req, nil
}

func decodeFlexiblePayload(payload []byte, contentType string, out any) error {
	switch {
	case strings.Contains(strings.ToLower(contentType), "yaml"), strings.Contains(strings.ToLower(contentType), "yml"):
		return yaml.Unmarshal(payload, out)
	default:
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		return decoder.Decode(out)
	}
}

func parseEntries(raw any) ([]domain.ConfigUpdateEntry, error) {
	normalized := normalizeMap(raw)
	buf, err := json.Marshal(normalized)
	if err != nil {
		return nil, newValidationError(fmt.Sprintf("marshal import entries: %v", err))
	}
	var entries []domain.ConfigUpdateEntry
	if err := json.Unmarshal(buf, &entries); err != nil {
		return nil, newValidationError(fmt.Sprintf("invalid import entries: %v", err))
	}
	if len(entries) == 0 {
		return nil, newValidationError("entries must not be empty")
	}
	return entries, nil
}

func parseImportItem(key string, raw any) (domain.ConfigUpdateEntry, error) {
	entry := domain.ConfigUpdateEntry{
		Key: key,
	}

	switch typed := normalizeMap(raw).(type) {
	case map[string]any:
		rawValue, hasValue := typed["value"]
		if !hasValue {
			if hasImportMetadata(typed) {
				return domain.ConfigUpdateEntry{}, newValidationError(fmt.Sprintf("key %q: value is required", key))
			}
			value, inferredType, err := encodeConfigValue(typed, "")
			if err != nil {
				return domain.ConfigUpdateEntry{}, newValidationError(fmt.Sprintf("key %q: %v", key, err))
			}
			entry.Value = value
			entry.Type = inferredType
			return entry, nil
		}

		entry.IsSecret = asBool(typed["isSecret"])

		value, inferredType, err := encodeConfigValue(rawValue, asString(typed["type"]))
		if err != nil {
			return domain.ConfigUpdateEntry{}, newValidationError(fmt.Sprintf("key %q: %v", key, err))
		}
		entry.Value = value
		entry.Type = inferredType
		return entry, nil
	default:
		value, inferredType, err := encodeConfigValue(typed, "")
		if err != nil {
			return domain.ConfigUpdateEntry{}, newValidationError(fmt.Sprintf("key %q: %v", key, err))
		}
		entry.Value = value
		entry.Type = inferredType
		return entry, nil
	}
}

func encodeConfigValue(raw any, explicitType string) (string, string, error) {
	if explicitType != "" {
		value, err := encodeByType(raw, explicitType)
		return value, explicitType, err
	}

	switch typed := raw.(type) {
	case nil:
		return "", "", fmt.Errorf("value must not be null")
	case string:
		return typed, "string", nil
	case bool:
		return strconv.FormatBool(typed), "bool", nil
	case int:
		return strconv.Itoa(typed), "int", nil
	case int8, int16, int32, int64:
		return fmt.Sprintf("%d", typed), "int", nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", typed), "int", nil
	case float32:
		return trimFloat(float64(typed)), inferNumericType(float64(typed)), nil
	case float64:
		return trimFloat(typed), inferNumericType(typed), nil
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return typed.String(), "int", nil
		}
		return typed.String(), "float", nil
	case map[string]any, []any:
		buf, err := json.Marshal(typed)
		if err != nil {
			return "", "", fmt.Errorf("marshal json value: %w", err)
		}
		return string(buf), "json", nil
	default:
		normalized := normalizeMap(typed)
		switch converted := normalized.(type) {
		case map[string]any, []any:
			buf, err := json.Marshal(converted)
			if err != nil {
				return "", "", fmt.Errorf("marshal json value: %w", err)
			}
			return string(buf), "json", nil
		default:
			return fmt.Sprint(converted), "string", nil
		}
	}
}

func encodeByType(raw any, explicitType string) (string, error) {
	switch explicitType {
	case "string":
		return fmt.Sprint(raw), nil
	case "bool":
		switch typed := raw.(type) {
		case bool:
			return strconv.FormatBool(typed), nil
		default:
			value := fmt.Sprint(raw)
			if _, err := strconv.ParseBool(value); err != nil {
				return "", fmt.Errorf("invalid bool value: %w", err)
			}
			return value, nil
		}
	case "int":
		switch typed := raw.(type) {
		case json.Number:
			if _, err := typed.Int64(); err != nil {
				return "", fmt.Errorf("invalid int value: %w", err)
			}
			return typed.String(), nil
		case float64:
			if math.Trunc(typed) != typed {
				return "", fmt.Errorf("invalid int value: not an integer")
			}
			return strconv.FormatInt(int64(typed), 10), nil
		default:
			value := fmt.Sprint(raw)
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return "", fmt.Errorf("invalid int value: %w", err)
			}
			return value, nil
		}
	case "float":
		switch typed := raw.(type) {
		case json.Number:
			if _, err := typed.Float64(); err != nil {
				return "", fmt.Errorf("invalid float value: %w", err)
			}
			return typed.String(), nil
		case float64:
			return trimFloat(typed), nil
		default:
			value := fmt.Sprint(raw)
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return "", fmt.Errorf("invalid float value: %w", err)
			}
			return value, nil
		}
	case "json":
		if value, ok := raw.(string); ok {
			if !json.Valid([]byte(value)) {
				return "", fmt.Errorf("invalid json value")
			}
			return value, nil
		}
		buf, err := json.Marshal(normalizeMap(raw))
		if err != nil {
			return "", fmt.Errorf("invalid json value: %w", err)
		}
		return string(buf), nil
	default:
		return "", fmt.Errorf("unsupported type %q", explicitType)
	}
}

func normalizeMap(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[key] = normalizeMap(nested)
		}
		return normalized
	case map[any]any:
		normalized := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized[fmt.Sprint(key)] = normalizeMap(nested)
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, nested := range typed {
			normalized = append(normalized, normalizeMap(nested))
		}
		return normalized
	default:
		return value
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func asBool(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}

func inferNumericType(value float64) string {
	if math.Trunc(value) == value {
		return "int"
	}
	return "float"
}

func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func hasImportMetadata(value map[string]any) bool {
	_, hasType := value["type"]
	_, hasSecret := value["isSecret"]
	return hasType || hasSecret
}

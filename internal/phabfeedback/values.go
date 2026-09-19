package phabfeedback

import (
	"encoding/json"
	"fmt"
	"strconv"
)

func mapValue(value any) (map[string]any, bool) {
	if value == nil {
		return map[string]any{}, true
	}
	result, ok := value.(map[string]any)
	return result, ok
}

func sliceValue(value any) ([]any, bool) {
	if value == nil {
		return []any{}, true
	}
	result, ok := value.([]any)
	return result, ok
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if result, ok := value.(string); ok {
		return result
	}
	return fmt.Sprint(value)
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	case json.Number:
		number, err := strconv.Atoi(string(typed))
		return number, err == nil
	case string:
		number, err := strconv.Atoi(typed)
		return number, err == nil
	default:
		return 0, false
	}
}

package web

import (
	"errors"
	"fmt"
	"strings"
)

func normalizeJSONText(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		if strings.HasSuffix(s, "```") {
			s = strings.TrimSpace(strings.TrimSuffix(s, "```"))
		}
	}
	return s
}

func validateResponseFormatText(text string, format *responseFormat) (string, error) {
	text = normalizeJSONText(text)
	if format == nil || strings.TrimSpace(format.Type) == "" || format.Type == "text" {
		return text, nil
	}
	value, err := decodeExactJSONValue([]byte(text))
	if err != nil {
		return "", fmt.Errorf("response_format %s requires valid JSON: %w", format.Type, err)
	}
	switch format.Type {
	case "json_object":
		if _, ok := value.(map[string]any); !ok {
			return "", errors.New("response_format json_object requires a top-level JSON object")
		}
		return text, nil
	case "json_schema":
		schema, _ := format.JSONSchema["schema"].(map[string]any)
		if schema == nil {
			return "", errors.New("response_format json_schema requires json_schema.schema")
		}
		if err := validateWebSchemaValue(schema, value); err != nil {
			return "", fmt.Errorf("response_format json_schema validation failed: %w", err)
		}
		return text, nil
	default:
		return "", fmt.Errorf("unsupported response_format type %q", format.Type)
	}
}

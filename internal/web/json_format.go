package web

import (
	"errors"
	"fmt"
	"m365-native/internal/chathub"
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

func validateResponseFormatDefinition(format *responseFormat) error {
	if format == nil || strings.TrimSpace(format.Type) == "" || format.Type == "text" || format.Type == "json_object" {
		return nil
	}
	if format.Type != "json_schema" {
		return fmt.Errorf("unsupported response_format type %q", format.Type)
	}
	schema, _ := format.JSONSchema["schema"].(map[string]any)
	if schema == nil {
		return errors.New("response_format json_schema requires json_schema.schema")
	}
	if _, err := compileWebSchema(schema); err != nil {
		return fmt.Errorf("response_format json_schema is invalid: %w", err)
	}
	return nil
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

type resultTextEvidenceCandidate struct {
	text   string
	source string
}

func resultTextEvidenceCandidates(result chathub.Result) []resultTextEvidenceCandidate {
	source := result.TextSource
	if source == "" {
		source = "canonical"
	}
	out := []resultTextEvidenceCandidate{{text: result.Text, source: source}}

	// Once a higher layer has intentionally replaced Text with a safety or
	// protocol response, raw upstream candidates are evidence only and must not
	// be resurrected as caller-visible content.
	if result.FinalText != "" || result.StreamedText != "" {
		if result.Text != result.FinalText && result.Text != result.StreamedText {
			return out
		}
	}
	seen := map[string]bool{result.Text: true}
	for _, candidate := range []resultTextEvidenceCandidate{
		{text: result.FinalText, source: "final"},
		{text: result.StreamedText, source: "stream"},
	} {
		if strings.TrimSpace(candidate.text) == "" || seen[candidate.text] {
			continue
		}
		seen[candidate.text] = true
		out = append(out, candidate)
	}
	return out
}

func validateResponseFormatResultEvidence(result chathub.Result, format *responseFormat) (chathub.Result, string, error, string) {
	candidates := resultTextEvidenceCandidates(result)
	var primaryErr error
	for i, candidate := range candidates {
		candidateFormatted, candidateErr := validateResponseFormatText(candidate.text, format)
		if candidateErr != nil {
			if i == 0 {
				primaryErr = candidateErr
			}
			continue
		}
		if i > 0 {
			result.Text = candidate.text
			result.TextSource = candidate.source
		}
		return result, candidateFormatted, nil, candidate.source
	}
	source := "canonical"
	if len(candidates) > 0 {
		source = candidates[0].source
	}
	return result, "", primaryErr, source
}

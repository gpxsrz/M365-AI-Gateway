package mcp

import (
	"errors"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"m365-native/internal/jsonschemautil"
)

type Tool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	Meta         map[string]any `json:"_meta,omitempty"`
}

type CallResult struct {
	Content        []map[string]any `json:"content"`
	StructuredData map[string]any   `json:"structuredContent,omitempty"`
	IsError        bool             `json:"isError,omitempty"`
	Meta           map[string]any   `json:"_meta,omitempty"`
}

func compileToolSchema(schema map[string]any) (*jsonschema.Schema, error) {
	return jsonschemautil.Compile(schema, "urn:m365-copilot2api:mcp-tool-schema")
}

func validateToolValue(schema map[string]any, value any) error {
	compiled, err := compileToolSchema(schema)
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

func decodeExactJSON(raw []byte, target any) error {
	return jsonschemautil.DecodeExact(raw, target)
}

func normalizeCallResult(tool Tool, result CallResult) (CallResult, error) {
	if result.Content == nil {
		result.Content = []map[string]any{}
	}
	for _, block := range result.Content {
		kind, ok := block["type"].(string)
		if !ok {
			return CallResult{}, errors.New("content block type required")
		}
		switch kind {
		case "text":
			if _, ok := block["text"].(string); !ok {
				return CallResult{}, errors.New("text content required")
			}
		case "image", "audio":
			if _, ok := block["data"].(string); !ok {
				return CallResult{}, errors.New("encoded content data required")
			}
			if _, ok := block["mimeType"].(string); !ok {
				return CallResult{}, errors.New("content MIME type required")
			}
		case "resource_link":
			if _, ok := block["name"].(string); !ok {
				return CallResult{}, errors.New("resource link name required")
			}
			if _, ok := block["uri"].(string); !ok {
				return CallResult{}, errors.New("resource link URI required")
			}
		case "resource":
			resource, ok := block["resource"].(map[string]any)
			if !ok {
				return CallResult{}, errors.New("embedded resource required")
			}
			if _, ok := resource["uri"].(string); !ok {
				return CallResult{}, errors.New("embedded resource URI required")
			}
			_, hasText := resource["text"].(string)
			_, hasBlob := resource["blob"].(string)
			if hasText == hasBlob {
				return CallResult{}, errors.New("embedded resource must contain text or blob")
			}
		default:
			return CallResult{}, errors.New("unsupported content block type")
		}
	}
	if tool.OutputSchema != nil && !result.IsError {
		if result.StructuredData == nil {
			return CallResult{}, errors.New("structured content required by output schema")
		}
		if err := validateToolValue(tool.OutputSchema, result.StructuredData); err != nil {
			return CallResult{}, err
		}
	}
	return result, nil
}

func (r CallResult) Text() string {
	var out []string
	for _, block := range r.Content {
		if typ, _ := block["type"].(string); typ != "text" {
			continue
		}
		if text, _ := block["text"].(string); text != "" {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n")
}

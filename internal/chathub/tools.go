package chathub

import (
	"encoding/json"
)

type Tool struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function,omitempty"`
	// Ingress evidence is retained only for the active caller request. These
	// fields are intentionally excluded from canonical ChatHub/OpenAI
	// serialization.
	IngressRaw         json.RawMessage            `json:"-"`
	IngressExtensions  map[string]json.RawMessage `json:"-"`
	FunctionExtensions map[string]json.RawMessage `json:"-"`
}

func (t *Tool) UnmarshalJSON(raw []byte) error {
	type canonical struct {
		Type     string          `json:"type"`
		Function json.RawMessage `json:"function,omitempty"`
	}
	var decoded canonical
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	extensions := make(map[string]json.RawMessage)
	for name, value := range fields {
		if name == "type" || name == "function" {
			continue
		}
		extensions[name] = append(json.RawMessage(nil), value...)
	}
	functionExtensions := make(map[string]json.RawMessage)
	if len(decoded.Function) > 0 {
		var functionFields map[string]json.RawMessage
		if json.Unmarshal(decoded.Function, &functionFields) == nil {
			for name, value := range functionFields {
				switch name {
				case "name", "description", "parameters", "annotations":
					continue
				default:
					functionExtensions[name] = append(json.RawMessage(nil), value...)
				}
			}
		}
	}
	*t = Tool{
		Type:               decoded.Type,
		Function:           append(json.RawMessage(nil), decoded.Function...),
		IngressRaw:         append(json.RawMessage(nil), raw...),
		IngressExtensions:  extensions,
		FunctionExtensions: functionExtensions,
	}
	if len(t.IngressExtensions) == 0 {
		t.IngressExtensions = nil
	}
	if len(t.FunctionExtensions) == 0 {
		t.FunctionExtensions = nil
	}
	return nil
}

func (t Tool) MarshalJSON() ([]byte, error) {
	var functionFields map[string]json.RawMessage
	if len(t.Function) > 0 {
		if err := json.Unmarshal(t.Function, &functionFields); err != nil {
			return nil, err
		}
	}
	canonicalFunction := make(map[string]json.RawMessage)
	for _, name := range []string{"name", "description", "parameters", "annotations"} {
		if value, ok := functionFields[name]; ok {
			canonicalFunction[name] = value
		}
	}
	return json.Marshal(struct {
		Type     string                     `json:"type"`
		Function map[string]json.RawMessage `json:"function,omitempty"`
	}{Type: t.Type, Function: canonicalFunction})
}

func callerToolsForChoice(tools []Tool, choice any) []Tool {
	if mode, ok := choice.(string); ok && mode == "none" {
		return nil
	}
	return tools
}

func callerMCPForChoice(serverURL string, choice any) string {
	if mode, ok := choice.(string); ok && mode == "none" {
		return ""
	}
	return serverURL
}

func clientPlugins(tools []Tool, mcpServerURL string, disableBuiltInSearch ...bool) []any {
	plugins := make([]any, 0, len(tools)+2)
	searchDisabled := len(disableBuiltInSearch) > 0 && disableBuiltInSearch[0]
	if !searchDisabled {
		plugins = append(plugins, map[string]any{"Id": "BingWebSearch", "Source": "BuiltIn"})
	}
	if mcpServerURL != "" {
		plugins = append(plugins, map[string]any{
			"Id":                "mcp-gateway",
			"Source":            "MCPServer",
			"Description":       "MCP Gateway tools",
			"Transport":         "mcp",
			"TransportUrl":      mcpServerURL,
			"TransportProtocol": "https://copilot.microsoft.com/schemas/plugins/local/transport/1.0",
		})
	}
	for _, t := range tools {
		var f struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal(t.Function, &f) != nil || f.Name == "" {
			continue
		}
		plugins = append(plugins, map[string]any{"Id": f.Name, "Source": "Client", "Description": f.Description, "Parameters": f.Parameters})
	}
	return plugins
}

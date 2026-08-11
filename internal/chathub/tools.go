package chathub

import "encoding/json"

type Tool struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function,omitempty"`
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

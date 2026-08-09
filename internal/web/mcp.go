package web

import (
	"context"
	"errors"
	"m365-native/internal/mcp"
	"net/http"
)

func productionMCPProvider() mcp.ToolProvider {
	return mcp.NewStaticToolProvider([]mcp.Tool{{
		Name:        "wp6_echo",
		Description: "Returns the supplied value unchanged to verify MCP interoperability.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []any{"value"},
		},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []any{"value"},
		},
		Annotations: map[string]any{
			"readOnlyHint":    true,
			"destructiveHint": false,
			"idempotentHint":  true,
			"openWorldHint":   false,
		},
	}}, func(_ context.Context, name string, arguments map[string]any) (mcp.CallResult, error) {
		value, ok := arguments["value"].(string)
		if name != "wp6_echo" || !ok {
			return mcp.CallResult{}, errors.New("invalid echo arguments")
		}
		return mcp.CallResult{
			Content:        []map[string]any{{"type": "text", "text": "WP6_ECHO:" + value}},
			StructuredData: map[string]any{"value": value},
		}, nil
	})
}

func (s *Server) newMCPRuntime() *mcp.Server {
	return mcp.NewServer(mcp.ServerOptions{
		Provider:     productionMCPProvider(),
		OriginPolicy: s.validateMCPOrigin,
	})
}

func (s *Server) validateMCPOrigin(r *http.Request) error {
	if len(r.Header.Values("Origin")) == 0 {
		return nil
	}
	info, err := s.adminSecurity.inspect(r)
	if err != nil {
		return err
	}
	return validateAdminOrigin(r.Header, info)
}

func (s *Server) mcpRuntime() *mcp.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mcp == nil {
		s.mcp = s.newMCPRuntime()
	}
	return s.mcp
}

func (s *Server) mcpStreamable(w http.ResponseWriter, r *http.Request) {
	s.mcpRuntime().ServeStreamableHTTP(w, r, apiKeyOwner(r))
}

func (s *Server) mcpLegacySSE(w http.ResponseWriter, r *http.Request) {
	s.mcpRuntime().ServeLegacySSE(w, r, apiKeyOwner(r))
}

func (s *Server) mcpLegacyMessage(w http.ResponseWriter, r *http.Request) {
	s.mcpRuntime().ServeLegacyMessage(w, r, apiKeyOwner(r))
}

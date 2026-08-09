package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const CurrentProtocolVersion = "2026-07-28"

func modernProtocolRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.TrimSpace(r.Header.Get(ProtocolHeader)) == CurrentProtocolVersion
}

func (s *Server) serveOfficialModern(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.callTimeout)
	defer cancel()
	sdkServer, err := s.newOfficialSDKServer(ctx)
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "MCP tool registry unavailable")
		return
	}
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return sdkServer },
		&sdkmcp.StreamableHTTPOptions{
			Stateless:                    true,
			MaxRequestBodyBytes:          s.maxMessageBytes,
			PropagateRequestCancellation: true,
		},
	)
	handler.ServeHTTP(w, r)
}

func (s *Server) newOfficialSDKServer(ctx context.Context) (*sdkmcp.Server, error) {
	tools, err := boundedProviderCall(ctx, s, s.provider.ListTools)
	if err != nil {
		return nil, err
	}
	tools, err = normalizeTools(tools)
	if err != nil {
		return nil, err
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "m365-copilot2api", Version: "wp6-handoff-v16"}, nil)
	for _, tool := range tools {
		tool := tool
		sdkTool, err := sdkToolFromTool(tool)
		if err != nil {
			return nil, err
		}
		server.AddTool(sdkTool, func(callContext context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			arguments := map[string]any{}
			if len(request.Params.Arguments) > 0 && string(request.Params.Arguments) != "null" {
				decoded, decodeErr := decodeJSONObject(request.Params.Arguments)
				if decodeErr != nil {
					return nil, decodeErr
				}
				arguments = decoded
			}
			if err := validateToolValue(tool.InputSchema, arguments); err != nil {
				return nil, err
			}
			boundedContext, cancel := context.WithTimeout(callContext, s.callTimeout)
			defer cancel()
			result, callErr := boundedProviderCall(boundedContext, s, func(providerContext context.Context) (CallResult, error) {
				return s.provider.CallTool(providerContext, tool.Name, arguments)
			})
			if callErr != nil {
				message := "tool execution failed"
				switch {
				case errors.Is(callErr, context.DeadlineExceeded):
					message = "tool call timed out"
				case errors.Is(callErr, context.Canceled), errors.Is(callErr, errMCPServerClosed):
					message = "tool call cancelled"
				}
				return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: message}}, IsError: true}, nil
			}
			normalized, normalizeErr := normalizeCallResult(tool, result)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			return sdkCallToolResult(normalized)
		})
	}
	return server, nil
}

func sdkToolFromTool(tool Tool) (*sdkmcp.Tool, error) {
	encoded, err := json.Marshal(tool)
	if err != nil {
		return nil, err
	}
	var converted sdkmcp.Tool
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return nil, err
	}
	return &converted, nil
}

func sdkCallToolResult(result CallResult) (*sdkmcp.CallToolResult, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var converted sdkmcp.CallToolResult
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return nil, err
	}
	return &converted, nil
}

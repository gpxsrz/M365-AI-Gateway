package web

import (
	"bytes"
	"encoding/json"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHindsightLikeStrictSchemaIngressPreservesExtensionsWithoutProjection(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("hindsight-ingress-evidence")); err != nil {
		t.Fatal(err)
	}
	chat := &continuationChat{results: []chathub.Result{{
		Text:           `{"marker":"HINDSIGHT_INGRESS_OK"}`,
		ConversationID: "hindsight-ingress",
		SessionID:      "hindsight-ingress-session",
	}}}
	server.chat = chat
	body := `{
		"model":"m365-auto",
		"messages":[{"role":"user","content":"Return the required JSON marker.","future_memory_message":{"opaque":"PRIVATE_MEMORY_META"}}],
		"reasoning":{"effort":"xhigh","future_reasoning_control":{"opaque":"PRIVATE_REASONING_META"}},
		"response_format":{"type":"json_schema","json_schema":{"name":"memory","strict":true,"schema":{"type":"object","properties":{"marker":{"type":"string","const":"HINDSIGHT_INGRESS_OK"}},"required":["marker"],"additionalProperties":false}},"future_format_control":{"opaque":"PRIVATE_FORMAT_META"}},
		"future_memory_request":{"opaque":"PRIVATE_REQUEST_META"}
	}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()
	server.memoryOpenAIChat(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-M365-Preserved-Extension-Counts"); got != "top=1,message=1,item=0,content=0,tool=0,format=1,reasoning=1" {
		t.Fatalf("preserved counts=%q", got)
	}
	for _, want := range []string{"top:future_memory_request", "message:future_memory_message", "format:future_format_control", "reasoning:future_reasoning_control"} {
		if !strings.Contains(rr.Header().Get("X-M365-Preserved-Extension-Names"), want) {
			t.Fatalf("preserved names=%q missing %q", rr.Header().Get("X-M365-Preserved-Extension-Names"), want)
		}
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream calls=%d", len(chat.requests))
	}
	for _, secret := range []string{"PRIVATE_MEMORY_META", "PRIVATE_REASONING_META", "PRIVATE_FORMAT_META", "PRIVATE_REQUEST_META", "future_memory_message", "future_reasoning_control", "future_format_control", "future_memory_request"} {
		if strings.Contains(chat.requests[0].Text, secret) {
			t.Fatalf("unsupported Hindsight ingress evidence leaked into ChatHub prompt: %q", secret)
		}
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != `{"marker":"HINDSIGHT_INGRESS_OK"}` {
		t.Fatalf("strict schema response changed: %s", rr.Body.String())
	}
}

func TestSemanticaLikeNestedToolSchemaAndStructuredResultRemainLossless(t *testing.T) {
	const exactID = "9007199254740993"
	longPayload := strings.Repeat("S", 9000)
	arguments := mustJSON(map[string]any{
		"query":    "decision",
		"metadata": map[string]any{"opaque_id": json.Number(exactID)},
	})
	structuredResult := mustJSON(map[string]any{
		"result": "SEMANTICA_TEXT_RESULT",
		"structuredContent": map[string]any{
			"opaque_id": json.Number(exactID),
			"blob":      longPayload,
		},
	})
	bodyValue := map[string]any{
		"model": "gpt-5.6-reasoning",
		"messages": []any{
			map[string]any{"role": "user", "content": "Inspect the Semantica graph."},
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{map[string]any{
					"id":   "call_semantica",
					"type": "function",
					"function": map[string]any{
						"name":                      "mcp__semantica__query_decisions",
						"arguments":                 arguments,
						"future_call_function_meta": "PRIVATE_CALL_FUNCTION_META",
					},
					"future_call_meta": "PRIVATE_CALL_META",
				}},
				"future_assistant_state": "PRIVATE_ASSISTANT_META",
			},
			map[string]any{
				"role":                   "tool",
				"tool_call_id":           "call_semantica",
				"content":                structuredResult,
				"future_mcp_result_meta": map[string]any{"resource": "PRIVATE_MCP_META"},
			},
		},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "mcp__semantica__query_decisions",
				"description": "Query Semantica decisions",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
						"metadata": map[string]any{
							"type":               "object",
							"x-semantica-schema": map[string]any{"opaque_id": json.Number(exactID)},
						},
					},
					"x-semantica-root": "PRESERVE_SCHEMA",
				},
				"future_function_control": "PRIVATE_FUNCTION_META",
			},
			"future_tool_state": map[string]any{"server": "semantica", "private": "PRIVATE_TOOL_META"},
		}},
	}
	raw, err := json.Marshal(bodyValue)
	if err != nil {
		t.Fatal(err)
	}
	var decoded oaiReq
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tools) != 1 || !bytes.Contains(decoded.Tools[0].IngressExtensions["future_tool_state"], []byte("PRIVATE_TOOL_META")) ||
		!bytes.Contains(decoded.Tools[0].FunctionExtensions["future_function_control"], []byte("PRIVATE_FUNCTION_META")) {
		t.Fatalf("Semantica tool evidence lost: %+v", decoded.Tools)
	}
	if len(decoded.Messages) != 3 || !bytes.Contains(decoded.Messages[1].IngressRaw, []byte("PRIVATE_CALL_META")) ||
		!bytes.Contains(decoded.Messages[2].IngressExtensions["future_mcp_result_meta"], []byte("PRIVATE_MCP_META")) {
		t.Fatalf("Semantica message evidence lost: %+v", decoded.Messages)
	}
	if calls := decoded.Messages[1].ToolCalls; len(calls) != 1 {
		t.Fatalf("tool calls=%v", calls)
	} else {
		encoded, _ := json.Marshal(calls[0])
		if bytes.Contains(encoded, []byte("future_call_meta")) || bytes.Contains(encoded, []byte("future_call_function_meta")) {
			t.Fatalf("unsupported nested tool-call metadata leaked into canonical call: %s", encoded)
		}
		if !bytes.Contains(encoded, []byte(exactID)) {
			t.Fatalf("tool arguments lost exact identifier: %s", encoded)
		}
	}
	definitions := toolDefinitionMaps(decoded.Tools)
	defs, _ := json.Marshal(definitions)
	if bytes.Contains(defs, []byte("future_function_control")) || bytes.Contains(defs, []byte("future_tool_state")) || bytes.Contains(defs, []byte("PRIVATE_FUNCTION_META")) {
		t.Fatalf("opaque Semantica tool extension leaked into router definition: %s", defs)
	}
	for _, want := range []string{"x-semantica-root", "x-semantica-schema", exactID, "PRESERVE_SCHEMA"} {
		if !bytes.Contains(defs, []byte(want)) {
			t.Fatalf("supported nested tool schema lost %q: %s", want, defs)
		}
	}
	prompt, _ := flattenPromptMessages(decoded.Messages, nil)
	for _, want := range []string{"SEMANTICA_TEXT_RESULT", exactID, longPayload} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Semantica structured tool result lost %q", want[:min(len(want), 40)])
		}
	}
	for _, forbidden := range []string{"PRIVATE_ASSISTANT_META", "PRIVATE_MCP_META", "PRIVATE_CALL_META", "PRIVATE_CALL_FUNCTION_META"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("unsupported Semantica metadata leaked into ChatHub prompt: %q", forbidden)
		}
	}
	checkpoint, err := canonicalCheckpointMessageWithToolName(decoded.Messages[2], "mcp__semantica__query_decisions")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(checkpoint, []byte("SEMANTICA_TEXT_RESULT")) || !bytes.Contains(checkpoint, []byte(exactID)) || bytes.Contains(checkpoint, []byte("PRIVATE_MCP_META")) {
		t.Fatalf("checkpoint projection violated evidence boundary: %s", checkpoint)
	}
}

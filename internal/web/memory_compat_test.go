package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func TestCompatibilityNamespacesAreIsolated(t *testing.T) {
	if !memoryCompatibilityRequest("/memory/v1/chat/completions") {
		t.Fatal("memory namespace was not detected")
	}
	if !hermesCompatibilityRequest("/hermes/v1/chat/completions") {
		t.Fatal("Hermes namespace was not detected")
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/models", "/api/chat"} {
		if memoryCompatibilityRequest(path) || hermesCompatibilityRequest(path) {
			t.Fatalf("ordinary route %q entered a compatibility profile", path)
		}
	}
	memory, ok := compatibilityCheckpointControl("/memory/v1/chat/completions")
	if !ok || memory.Namespace != "memory-provider" || !memory.ForceNew {
		t.Fatalf("memory checkpoint control = %#v, %t", memory, ok)
	}
	hermes, ok := compatibilityCheckpointControl("/hermes/v1/chat/completions")
	if !ok || hermes.Namespace != "hermes" || hermes.ForceNew {
		t.Fatalf("Hermes checkpoint control = %#v, %t", hermes, ok)
	}
}

func TestHermesCompatibilityToggleControlsOnlyDedicatedProfileRoute(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	server.chat = &captureSingleAccountChat{}
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`
	settings := server.settings.get()
	settings.HermesCompatibilityEnabled = false
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}

	disabled := httptest.NewRecorder()
	server.hermesOpenAIChat(disabled, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if disabled.Code != http.StatusServiceUnavailable || !strings.Contains(disabled.Body.String(), "Hermes compatibility profile is disabled") {
		t.Fatalf("disabled status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	settings.HermesCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("hermes-profile")); err != nil {
		t.Fatal(err)
	}
	enabled := httptest.NewRecorder()
	enabledRequest := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), "hermes-owner")
	server.hermesOpenAIChat(enabled, enabledRequest)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enabled status=%d body=%s", enabled.Code, enabled.Body.String())
	}
}

func TestHermesTextPolicyOverflowUsesRecoverableContextCodeOnlyOnDedicatedRoute(t *testing.T) {
	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	body := `{"model":"gpt-5.6-sol","session_key":"hermes-text-policy","messages":[{"role":"user","content":` + mustJSON(over) + `}]}`
	server := newAdminSecurityServer(t, "administrator-password")
	server.chat = &captureSingleAccountChat{}

	hermes := httptest.NewRecorder()
	hermesRequest := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), "hermes-owner")
	server.hermesOpenAIChat(hermes, hermesRequest)
	if hermes.Code != http.StatusBadRequest || wp1ErrorCode(t, hermes) != "context_length_exceeded" {
		t.Fatalf("Hermes status=%d code=%q body=%s", hermes.Code, wp1ErrorCode(t, hermes), hermes.Body.String())
	}
	hermesError := strings.ToLower(hermes.Body.String())
	if !strings.Contains(hermesError, "input is too long") || !strings.Contains(hermes.Body.String(), "128000") || !strings.Contains(hermesError, "utf-16") {
		t.Fatalf("Hermes compatibility error lacks the recoverable marker or real UTF-16 policy: %s", hermes.Body.String())
	}
	requireCallerTextRecoveryMetadata(t, textPolicyErrorObject(t, hermes), "context_length_exceeded", defaultTextInputLimitUTF16+1)

	generic := httptest.NewRecorder()
	genericRequest := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), "generic-owner")
	server.openaiChat(generic, genericRequest)
	if generic.Code != http.StatusBadRequest || wp1ErrorCode(t, generic) != "text_policy_exceeded" {
		t.Fatalf("generic status=%d code=%q body=%s", generic.Code, wp1ErrorCode(t, generic), generic.Body.String())
	}
	requireCallerTextRecoveryMetadata(t, textPolicyErrorObject(t, generic), "text_policy_exceeded", defaultTextInputLimitUTF16+1)
}

func TestMemoryCompatibilityRouteRequiresExplicitOptIn(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	server.chat = &captureSingleAccountChat{}
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"remember this"}]}`

	disabled := httptest.NewRecorder()
	server.memoryOpenAIChat(disabled, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if disabled.Code != http.StatusServiceUnavailable || !strings.Contains(disabled.Body.String(), "Memory Provider compatibility profile is disabled") {
		t.Fatalf("disabled status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-opt-in")); err != nil {
		t.Fatal(err)
	}
	enabled := httptest.NewRecorder()
	server.memoryOpenAIChat(enabled, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if enabled.Code != http.StatusOK {
		t.Fatalf("enabled status=%d body=%s", enabled.Code, enabled.Body.String())
	}
}

func TestMemorySchemaInstructionPinsProtocolKeys(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"city": map[string]any{"type": "string"}},
			"required":             []any{"city"},
			"additionalProperties": false,
		},
	}}
	got := memorySchemaInstruction(format)
	for _, want := range []string{"copy them exactly", "never translate", `"city"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("instruction missing %q: %s", want, got)
		}
	}
}

type memoryWrappedJSONChat struct {
	calls int
	text  string
}

func (c *memoryWrappedJSONChat) Chat(_ context.Context, _ chathub.Account, _ chathub.Request) (chathub.Result, error) {
	c.calls++
	text := c.text
	if text == "" {
		text = "Here is the requested object:\n{\"city\":\"台中\"}"
	}
	return chathub.Result{
		Text:           text,
		ConversationID: "memory-wrapped-json",
		SessionID:      "memory-wrapped-json-session",
	}, nil
}

func (c *memoryWrappedJSONChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func (c *memoryWrappedJSONChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func TestMemorySchemaAcceptsSingleJSONValueWrappedInProse(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-wrapped-json")); err != nil {
		t.Fatal(err)
	}
	chat := &memoryWrappedJSONChat{}
	server.chat = chat
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"我住台中"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()

	server.memoryOpenAIChat(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if chat.calls != 1 {
		t.Fatalf("chat calls=%d, want 1 deterministic extraction without repair", chat.calls)
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
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != `{"city":"台中"}` {
		t.Fatalf("response did not return the extracted JSON only: %s", rr.Body.String())
	}
}

func TestMemorySchemaRejectsMultipleWrappedJSONValuesWithoutRepair(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-multiple-json")); err != nil {
		t.Fatal(err)
	}
	chat := &memoryWrappedJSONChat{text: "First:\n{\"city\":\"台中\"}\nSecond:\n{\"city\":\"新竹\"}"}
	server.chat = chat
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"我住台中"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()

	server.memoryOpenAIChat(rr, req)

	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "response_format_validation_failed") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if chat.calls != 1 {
		t.Fatalf("chat calls=%d, want 1 fail-closed response without repair", chat.calls)
	}
}

func TestMemorySchemaIgnoresIncidentalScalarsAroundWrappedObject(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-numbered-wrapper")); err != nil {
		t.Fatal(err)
	}
	chat := &memoryWrappedJSONChat{text: "1. The requested object is:\n{\"city\":\"台中\"}\nThis is true."}
	server.chat = chat
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"我住台中"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()

	server.memoryOpenAIChat(rr, req)

	if rr.Code != http.StatusOK || chat.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rr.Code, chat.calls, rr.Body.String())
	}
}

func TestMemorySchemaDoesNotPromoteScalarFromProse(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-prose-scalar")); err != nil {
		t.Fatal(err)
	}
	chat := &memoryWrappedJSONChat{text: "The count is 7."}
	server.chat = chat
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"count"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"integer"}}}}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()

	server.memoryOpenAIChat(rr, req)

	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "response_format_validation_failed") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if chat.calls != 1 {
		t.Fatalf("chat calls=%d, want 1 fail-closed response without repair", chat.calls)
	}
}

type memoryWrappedRepairChat struct {
	calls   int
	prompts []string
}

func (c *memoryWrappedRepairChat) Chat(_ context.Context, _ chathub.Account, req chathub.Request) (chathub.Result, error) {
	c.calls++
	c.prompts = append(c.prompts, req.Text)
	if c.calls == 1 {
		return chathub.Result{
			Text:           "Here is the requested object:\n```json\n{\"城市\":\"台中\"}\n```",
			ConversationID: "memory-wrapped-repair",
			SessionID:      "memory-wrapped-repair-session",
		}, nil
	}
	return chathub.Result{
		Text:           `{"city":"台中"}`,
		ConversationID: "memory-wrapped-repair",
		SessionID:      "memory-wrapped-repair-session",
	}, nil
}

func (c *memoryWrappedRepairChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func (c *memoryWrappedRepairChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func TestMemorySchemaRepairUsesOnlyExtractedJSONCandidate(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-wrapped-repair")); err != nil {
		t.Fatal(err)
	}
	chat := &memoryWrappedRepairChat{}
	server.chat = chat
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"我住台中"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()

	server.memoryOpenAIChat(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if chat.calls != 2 || len(chat.prompts) != 2 {
		t.Fatalf("chat calls=%d prompts=%d, want one candidate plus one bounded repair", chat.calls, len(chat.prompts))
	}
	if strings.Contains(chat.prompts[1], "Here is the requested object") || strings.Contains(chat.prompts[1], "```json") {
		t.Fatalf("repair prompt retained wrapper text: %s", chat.prompts[1])
	}
	if !strings.Contains(chat.prompts[1], "PREVIOUS_CANDIDATE:\n{\"城市\":\"台中\"}") {
		t.Fatalf("repair prompt did not use the extracted candidate: %s", chat.prompts[1])
	}
}

func TestMemorySchemaRepairPromptIsBoundedToPreviousCandidate(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{"type": "object", "additionalProperties": false},
	}}
	got := memorySchemaRepairPrompt(`{"城市":"台中"}`, format, errors.New("unknown property"))
	for _, want := range []string{"Repair the PREVIOUS_CANDIDATE only", "must preserve the exact container structure, property order, and scalar values", `{"城市":"台中"}`, "unknown property"} {
		if !strings.Contains(got, want) {
			t.Fatalf("repair prompt missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "ORIGINAL_REQUEST") {
		t.Fatalf("repair prompt can answer the original request again: %s", got)
	}
}

func TestMemoryRepairPreservesFactsAllowsKeyRepair(t *testing.T) {
	if err := memoryRepairPreservesFacts(`{"城市":"台中","語言":"繁體中文"}`, `{"city":"台中","language":"繁體中文"}`); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryRepairPreservesFactsRejectsAmbiguousSchemaAssociation(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city":     map[string]any{"type": "string"},
				"language": map[string]any{"type": "string"},
			},
			"required": []any{"city", "language"},
		},
	}}
	if err := memoryRepairPreservesFacts(`{"城市":"台中","語言":"繁體中文"}`, `{"language":"台中","city":"繁體中文"}`, format); err == nil {
		t.Fatal("accepted ambiguous scalar-to-property swap")
	}
}

func TestMemoryRepairPreservesFactsAllowsProvableSchemaAssociation(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
				"age":  map[string]any{"type": "integer"},
			},
			"required": []any{"city", "age"},
		},
	}}
	if err := memoryRepairPreservesFacts(`{"城市":"台中","年齡":30}`, `{"city":"台中","age":30}`, format); err != nil {
		t.Fatalf("rejected provable key repair: %v", err)
	}
}

func TestMemoryRepairPreservesFactsRejectsInventedOrDuplicatedValues(t *testing.T) {
	for _, repaired := range []string{`{"city":"新竹"}`, `{"city":"台中","previous_city":"台中"}`} {
		if err := memoryRepairPreservesFacts(`{"城市":"台中"}`, repaired); err == nil {
			t.Fatalf("accepted unsafe repair %s", repaired)
		}
	}
}

func TestMemoryRepairPreservesFactsRejectsMalformedCandidate(t *testing.T) {
	if err := memoryRepairPreservesFacts(`not-json`, `{"city":"台中"}`); err == nil {
		t.Fatal("accepted repair from malformed candidate")
	}
}

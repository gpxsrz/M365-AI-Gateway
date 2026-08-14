package web

import (
	"bytes"
	"context"
	"encoding/json"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIIngressPreservesUnknownEvidenceBeforeProjection(t *testing.T) {
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"hello"},
				{"type":"future_semantic_block","payload":{"id":9007199254740993}}
			],
			"future_message_state":{"cursor":"opaque-message"}
		}],
		"tools":[{
			"type":"function",
			"function":{"name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer"}}}},
			"future_tool_annotations":{"readOnlyHint":true}
		}],
		"future_request_extension":{"opaque":"keep-me","large_id":9007199254740993}
	}`
	request := httptest.NewRequest("POST", "/hermes/v1/chat/completions", strings.NewReader(body))
	var decoded oaiReq
	if err := decodeBoundedJSON(httptest.NewRecorder(), request, 1<<20, &decoded); err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(decoded.IngressRaw, []byte(`"future_request_extension"`)) {
		t.Fatalf("raw request evidence lost: %s", decoded.IngressRaw)
	}
	requestExt := decoded.IngressExtensions["future_request_extension"]
	if !bytes.Contains(requestExt, []byte(`9007199254740993`)) || !bytes.Contains(requestExt, []byte(`"keep-me"`)) {
		t.Fatalf("request extension not preserved losslessly: %s", requestExt)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("messages=%d", len(decoded.Messages))
	}
	message := decoded.Messages[0]
	if !bytes.Contains(message.IngressRaw, []byte(`"future_message_state"`)) ||
		!bytes.Contains(message.IngressExtensions["future_message_state"], []byte(`"opaque-message"`)) {
		t.Fatalf("message extension not preserved: raw=%s extensions=%v", message.IngressRaw, message.IngressExtensions)
	}
	if !bytes.Contains(message.ContentRaw, []byte(`"future_semantic_block"`)) {
		t.Fatalf("raw content evidence lost: %s", message.ContentRaw)
	}
	if len(message.UnknownContentParts) != 1 ||
		!bytes.Contains(message.UnknownContentParts[0], []byte(`9007199254740993`)) {
		t.Fatalf("unknown content parts=%q", message.UnknownContentParts)
	}
	if len(message.UnknownContentTypes) != 1 || message.UnknownContentTypes[0] != "future_semantic_block" {
		t.Fatalf("unknown content types=%v", message.UnknownContentTypes)
	}
	if len(decoded.Tools) != 1 {
		t.Fatalf("tools=%d", len(decoded.Tools))
	}
	if !bytes.Contains(decoded.Tools[0].IngressRaw, []byte(`"future_tool_annotations"`)) ||
		!bytes.Contains(decoded.Tools[0].IngressExtensions["future_tool_annotations"], []byte(`"readOnlyHint":true`)) {
		t.Fatalf("tool extension not preserved: raw=%s extensions=%v", decoded.Tools[0].IngressRaw, decoded.Tools[0].IngressExtensions)
	}

	// Canonical request behavior must remain intact.
	if decoded.Model != "gpt-5.6-reasoning" || decoded.Messages[0].Role != "user" {
		t.Fatalf("canonical fields changed: %#v", decoded)
	}
	if got, _ := decoded.Messages[0].Content.([]any); len(got) != 1 {
		t.Fatalf("canonical content changed: %#v", decoded.Messages[0].Content)
	}
}

func TestOpenAIIngressEvidenceIsNotSerializedByCanonicalProjection(t *testing.T) {
	raw := []byte(`{
		"model":"m",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"future_private_part","value":"caller-owned"}],"private_future_state":{"secret_like":"caller-owned"}}],
		"future_request_extension":{"opaque":"caller-owned"}
	}`)
	var decoded oaiReq
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.IngressExtensions) != 1 || len(decoded.Messages[0].IngressExtensions) != 1 {
		t.Fatalf("test fixture evidence was not retained")
	}

	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("future_request_extension")) || bytes.Contains(canonical, []byte("private_future_state")) {
		t.Fatalf("request-scoped raw evidence leaked into canonical serialization: %s", canonical)
	}

	history, err := canonicalCheckpointMessages(decoded.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var baseline oaiReq
	baselineDecoder := json.NewDecoder(strings.NewReader(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	baselineDecoder.UseNumber()
	if err := baselineDecoder.Decode(&baseline); err != nil {
		t.Fatal(err)
	}
	baselineHistory, err := canonicalCheckpointMessages(baseline.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(history.digests, ",") != strings.Join(baselineHistory.digests, ",") ||
		strings.Join(history.chains, ",") != strings.Join(baselineHistory.chains, ",") {
		t.Fatalf("request-scoped ingress evidence changed checkpoint identity: got=%v/%v baseline=%v/%v", history.digests, history.chains, baselineHistory.digests, baselineHistory.chains)
	}
	prompt, _ := flattenPromptMessages(decoded.Messages, nil)
	if strings.Contains(prompt, "private_future_state") || strings.Contains(prompt, "future_private_part") || strings.Contains(prompt, "caller-owned") {
		t.Fatalf("unsupported evidence leaked into ChatHub projection: %s", prompt)
	}
}

type ingressObservationChat struct {
	requests []chathub.Request
}

func (c *ingressObservationChat) Chat(_ context.Context, _ chathub.Account, req chathub.Request) (chathub.Result, error) {
	c.requests = append(c.requests, req)
	return chathub.Result{Text: "ok", ConversationID: "ingress-observation", SessionID: "session"}, nil
}

func (c *ingressObservationChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, emit func(string) error) (chathub.Result, error) {
	result, err := c.Chat(ctx, account, req)
	if err == nil && emit != nil {
		err = emit(result.Text)
	}
	return result, err
}

func (c *ingressObservationChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	result, err := c.Chat(ctx, account, req)
	if err == nil && emit != nil {
		err = emit(chathub.StreamEvent{Kind: "text", Text: result.Text})
	}
	return result, err
}

func TestOpenAIIngressUnknownEvidenceIsObservableButNotProjected(t *testing.T) {
	chat := &ingressObservationChat{}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"future_block","value":"PRIVATE_VALUE"}],"future_message":{"value":"PRIVATE_VALUE"}}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}},"future_tool":{"value":"PRIVATE_VALUE"}}],
		"future_top":{"value":"PRIVATE_VALUE"}
	}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-M365-Preserved-Extension-Counts"); got != "top=1,message=1,item=0,content=1,tool=1,format=0,reasoning=0" {
		t.Fatalf("preserved extension counts=%q", got)
	}
	gotNames := rr.Header().Get("X-M365-Preserved-Extension-Names")
	for _, want := range []string{"top:future_top", "message:future_message", "content:future_block", "tool:future_tool"} {
		if !strings.Contains(gotNames, want) {
			t.Fatalf("preserved names=%q missing %q", gotNames, want)
		}
	}
	if len(chat.requests) == 0 {
		t.Fatal("no upstream request observed")
	}
	for _, req := range chat.requests {
		if strings.Contains(req.Text, "future_top") || strings.Contains(req.Text, "future_message") || strings.Contains(req.Text, "future_block") || strings.Contains(req.Text, "PRIVATE_VALUE") {
			t.Fatalf("unsupported extension leaked into ChatHub prompt: %s", req.Text)
		}
	}
	for _, expose := range []string{"X-M365-Preserved-Extension-Counts", "X-M365-Preserved-Extension-Names"} {
		if !strings.Contains(rr.Header().Get("Access-Control-Expose-Headers"), expose) {
			t.Fatalf("%s not exposed: %s", expose, rr.Header().Get("Access-Control-Expose-Headers"))
		}
	}
}

func TestOpenAIIngressUnsafeExtensionNamesAreCountedButNotReflected(t *testing.T) {
	var body oaiReq
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hello","unsafe field":"x"}],"bad field":"y"}`)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	summary := setCallerIngressEvidenceHeaders(rr, body)
	if summary.TopLevel != 1 || summary.Message != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	if got := rr.Header().Get("X-M365-Preserved-Extension-Names"); strings.Contains(got, "bad field") || strings.Contains(got, "unsafe field") {
		t.Fatalf("unsafe caller field name reflected into header: %q", got)
	}
}

func TestOpenAIIngressMissingTypeContentRequiresTextToBeCanonical(t *testing.T) {
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":[{"text":"legacy text"},{"future":"opaque"}]}]}`)
	var decoded oaiReq
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("messages=%d", len(decoded.Messages))
	}
	parts, _ := decoded.Messages[0].Content.([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "legacy text" {
		t.Fatalf("canonical parts=%#v", parts)
	}
	if len(decoded.Messages[0].UnknownContentParts) != 1 || decoded.Messages[0].UnknownContentTypes[0] != "<missing>" {
		t.Fatalf("unknown parts=%v types=%v", decoded.Messages[0].UnknownContentParts, decoded.Messages[0].UnknownContentTypes)
	}
}

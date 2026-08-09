package web

import (
	"context"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type phase6FilesChat struct {
	accounts []chathub.Account
	requests []chathub.Request
}

func (c *phase6FilesChat) next(account chathub.Account, request chathub.Request) chathub.Result {
	c.accounts = append(c.accounts, account)
	c.requests = append(c.requests, request)
	return chathub.Result{Text: "ok", ConversationID: firstNonEmpty(request.ConversationID, "conversation-files"), SessionID: firstNonEmpty(request.SessionID, "session-files")}
}

func (c *phase6FilesChat) Chat(_ context.Context, account chathub.Account, request chathub.Request) (chathub.Result, error) {
	return c.next(account, request), nil
}

func (c *phase6FilesChat) ChatWithDelta(_ context.Context, account chathub.Account, request chathub.Request, emit func(string) error) (chathub.Result, error) {
	result := c.next(account, request)
	if emit != nil {
		_ = emit(result.Text)
	}
	return result, nil
}

func (c *phase6FilesChat) ChatWithEvents(_ context.Context, account chathub.Account, request chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	result := c.next(account, request)
	if emit != nil {
		_ = emit(chathub.StreamEvent{Kind: "text", Text: result.Text})
	}
	return result, nil
}

func phase6FilesServer(t *testing.T, chat *phase6FilesChat) *Server {
	t.Helper()
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	server.resourceToken = func(_ context.Context, scope string) (string, error) {
		if scope != graphResourceScope {
			t.Fatalf("resource scope=%q", scope)
		}
		return "graph-secondary-token", nil
	}
	return server
}

func TestWP6OpenAIInlineDocumentIsAttachmentOnlyAndGetsGraphToken(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":[{"type":"input_file","filename":"report.txt","file_data":"data:text/plain;base64,UkVQT1JU"}]}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 1 || len(chat.requests[0].Attachments) != 1 {
		t.Fatalf("requests=%#v", chat.requests)
	}
	attachment := chat.requests[0].Attachments[0]
	if attachment.Type != "file" || attachment.Name != "report.txt" || attachment.URL != "data:text/plain;base64,UkVQT1JU" {
		t.Fatalf("attachment=%#v", attachment)
	}
	if len(chat.accounts) != 1 || chat.accounts[0].GraphAccessToken != "graph-secondary-token" || chat.accounts[0].AccessToken != "test-token" {
		t.Fatalf("account=%#v", chat.accounts)
	}
}

func TestWP6ResponsesInputFileUsesSharedDocumentPath(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	body := `{"model":"gpt-5.6-sol","input":[{"role":"user","content":[{"type":"input_file","filename":"data.csv","file_data":"data:text/csv;base64,YSwxCg=="}]}]}`
	recorder := httptest.NewRecorder()
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 1 || len(chat.requests[0].Attachments) != 1 || chat.requests[0].Attachments[0].Name != "data.csv" {
		t.Fatalf("requests=%#v", chat.requests)
	}
}

func TestWP6AttachmentPolicyRejectsFourthBeforeAccountOrChat(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	resourceCalls := 0
	server.resourceToken = func(context.Context, string) (string, error) {
		resourceCalls++
		return "token", nil
	}
	attachment := `{"type":"file","name":"a.txt","url":"data:text/plain;base64,QQ=="}`
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"read"}],"attachments":[` + strings.Join([]string{attachment, attachment, attachment, attachment}, ",") + `]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "shared limit of 3") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 || resourceCalls != 0 {
		t.Fatalf("invalid request crossed trust boundary: chat=%d resource=%d", len(chat.requests), resourceCalls)
	}
}

func TestWP6UnresolvedFileIDIsClearError(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":[{"type":"input_file","filename":"report.txt","file_id":"file_123"}]}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "file_id is unsupported") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("unresolved file_id reached chat: %#v", chat.requests)
	}
}

func TestWP6LegacyStreamAcceptsAttachmentOnly(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	body := `{"attachments":[{"type":"file","name":"a.txt","url":"data:text/plain;base64,QQ=="}],"sessionKey":"files-stream"}`
	recorder := httptest.NewRecorder()
	server.chatStream(recorder, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(body)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "event: done") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 1 || chat.requests[0].Text != "" || chat.accounts[0].GraphAccessToken == "" {
		t.Fatalf("request=%#v account=%#v", chat.requests, chat.accounts)
	}
}

func TestWP6ImageDetailAndVerbosityAreSortedDowngrades(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	image := "data:image/png;base64,iVBORw0KGgo="
	body := `{"model":"gpt-5.6-sol","verbosity":"high","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + image + `","detail":"original"}}]}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-M365-Downgraded-Parameters"); got != "image_detail,verbosity" {
		t.Fatalf("downgrade header=%q", got)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-M365-Downgraded-Parameters") {
		t.Fatalf("exposed headers=%q", got)
	}
	if len(chat.requests) != 1 || len(chat.requests[0].Attachments) != 1 || chat.requests[0].Attachments[0].Detail != "" {
		t.Fatalf("request=%#v", chat.requests)
	}
	if strings.Contains(strings.ToLower(chat.requests[0].Text), "verbosity") || strings.Contains(strings.ToLower(chat.requests[0].Text), "image_detail") || strings.Contains(strings.ToLower(chat.requests[0].Text), "original") {
		t.Fatalf("downgrade rewrote prompt: %q", chat.requests[0].Text)
	}
}

func TestWP6InvalidImageDetailAndVerbosityFailBeforeChat(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo=","detail":"maximum"}}]}]}`,
		`{"model":"gpt-5.6-sol","verbosity":"maximum","messages":[{"role":"user","content":"hello"}]}`,
	} {
		chat := &phase6FilesChat{}
		server := phase6FilesServer(t, chat)
		recorder := httptest.NewRecorder()
		server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
		if recorder.Code != http.StatusBadRequest || len(chat.requests) != 0 {
			t.Fatalf("status=%d body=%s requests=%d", recorder.Code, recorder.Body.String(), len(chat.requests))
		}
	}
}

func TestWP6ResponsesImageIsParsedOnceAndExposesDowngrade(t *testing.T) {
	chat := &phase6FilesChat{}
	server := phase6FilesServer(t, chat)
	body := `{"model":"gpt-5.6-sol","verbosity":"low","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo=","detail":"high"}]}]}`
	recorder := httptest.NewRecorder()
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-M365-Downgraded-Parameters"); got != "image_detail,verbosity" {
		t.Fatalf("downgrade header=%q", got)
	}
	if len(chat.requests) != 1 || len(chat.requests[0].Attachments) != 1 {
		t.Fatalf("Responses image duplicated: %#v", chat.requests)
	}
}

func TestWP6ResponsesPrecheckDoesNotCountHistoricalAttachmentsAsActive(t *testing.T) {
	parts := func(label string) []any {
		return []any{map[string]any{
			"type": "input_image", "image_url": "data:image/png;base64," + label, "detail": "high",
		}}
	}
	request := oaiReq{Verbosity: "low", Messages: []oaiMsg{
		{Role: "user", Content: parts("AAAA")},
		{Role: "assistant", Content: "one"},
		{Role: "user", Content: parts("BBBB")},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: parts("CCCC")},
		{Role: "assistant", Content: "three"},
		{Role: "user", Content: parts("DDDD")},
	}}
	downgraded, err := adapterCompatibilityParameters(request)
	if err != nil || strings.Join(downgraded, ",") != "image_detail,verbosity" {
		t.Fatalf("full-history precheck downgraded=%v err=%v", downgraded, err)
	}
	_, active := parseContent(request.Messages[len(request.Messages)-1].Content)
	if _, err := normalizeCompatibilityParameters(active, request.Verbosity); err != nil {
		t.Fatalf("single outbound attachment rejected after checkpoint: %v", err)
	}
	all := make([]chathub.Attachment, 0, 4)
	for _, message := range request.Messages {
		_, attachments := parseContent(message.Content)
		all = append(all, attachments...)
	}
	if _, err := normalizeCompatibilityParameters(all, request.Verbosity); err == nil {
		t.Fatal("four truly active attachments bypassed shared quota")
	}
}

package chathub

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"
)

func TestClientPrivateModeDefaultsAndReevaluates(t *testing.T) {
	client := NewClient()
	if !client.privateModeEnabled() {
		t.Fatal("fresh ChatHub client must fail private")
	}
	private := false
	client.PrivateMode = func() bool { return private }
	if client.privateModeEnabled() {
		t.Fatal("normal mode callback was ignored")
	}
	private = true
	if !client.privateModeEnabled() {
		t.Fatal("mode callback was not re-evaluated")
	}
}

func TestBuildWSURLAppliesPrivateModeToEveryNewConnection(t *testing.T) {
	account := Account{AccessToken: "token", OID: "oid", TID: "tid"}
	for _, requestID := range []string{"request-one", "request-two"} {
		raw, err := buildWSURL(account, "session", "conversation", requestID, true)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if values := parsed.Query()["disableMemory"]; len(values) != 1 || values[0] != "1" {
			t.Fatalf("private URL disableMemory=%#v", values)
		}
	}

	raw, err := buildWSURL(account, "session", "conversation", "request-normal", false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := parsed.Query()["disableMemory"]; exists {
		t.Fatalf("normal URL exposed disableMemory: %s", raw)
	}
}

func TestPrivateModeReappliesOnReconnectAndToolContinuationDialPaths(t *testing.T) {
	stopDial := errors.New("stop before network")
	var queries []url.Values
	client := NewClient()
	client.Dialer = &websocket.Dialer{Proxy: func(request *http.Request) (*url.URL, error) {
		queries = append(queries, request.URL.Query())
		return nil, stopDial
	}}
	private := true
	client.PrivateMode = func() bool { return private }
	account := Account{AccessToken: "token", OID: "oid", TID: "tid"}

	requests := []Request{
		{Text: "reconnect delta", ConversationID: "conversation", SessionID: "session-one"},
		{Text: "[tool]\nresult continuation", ConversationID: "conversation", SessionID: "session-two"},
	}
	for _, request := range requests {
		if _, err := client.Chat(context.Background(), account, request); !errors.Is(err, stopDial) {
			t.Fatalf("dial error=%v, want sentinel", err)
		}
	}
	private = false
	if _, err := client.Chat(context.Background(), account, Request{Text: "normal", ConversationID: "conversation", SessionID: "session-three"}); !errors.Is(err, stopDial) {
		t.Fatalf("normal dial error=%v, want sentinel", err)
	}

	if len(queries) != 3 {
		t.Fatalf("captured dials=%d want=3", len(queries))
	}
	for i, query := range queries[:2] {
		if values := query["disableMemory"]; len(values) != 1 || values[0] != "1" {
			t.Fatalf("private dial %d disableMemory=%#v", i, values)
		}
	}
	if _, exists := queries[2]["disableMemory"]; exists {
		t.Fatalf("normal continuation exposed disableMemory: %#v", queries[2]["disableMemory"])
	}
}

func TestPrivateModeAppliesToFreshScratchConversationDial(t *testing.T) {
	stopDial := errors.New("stop before network")
	var query url.Values
	client := NewClient()
	client.Dialer = &websocket.Dialer{Proxy: func(request *http.Request) (*url.URL, error) {
		query = request.URL.Query()
		return nil, stopDial
	}}
	client.PrivateMode = func() bool { return true }
	account := Account{AccessToken: "token", OID: "oid", TID: "tid"}

	if _, err := client.Chat(context.Background(), account, Request{Text: "isolated router scratch", Started: true}); !errors.Is(err, stopDial) {
		t.Fatalf("scratch dial error=%v, want sentinel", err)
	}
	if values := query["disableMemory"]; len(values) != 1 || values[0] != "1" {
		t.Fatalf("scratch private dial disableMemory=%#v", values)
	}
	if query.Get("ConversationId") == "" || query.Get("X-SessionId") == "" {
		t.Fatalf("scratch dial did not generate fresh conversation/session identity: %#v", query)
	}
}

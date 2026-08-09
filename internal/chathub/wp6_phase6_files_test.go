package chathub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type phase6RoundTripFunc func(*http.Request) (*http.Response, error)

func (f phase6RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func phase6Response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d test", status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestWP6DocumentUploadUsesGraphChunksAndLocalFileAnnotation(t *testing.T) {
	raw := bytes.Repeat([]byte("a"), int(documentUploadChunkSize+17))
	dataURL := "data:text/plain;base64," + base64.StdEncoding.EncodeToString(raw)
	var mu sync.Mutex
	var ranges []string
	var chunks [][]byte
	var graphBody map[string]any
	client := &Client{
		HTTPHeader: http.Header{"User-Agent": {"phase6-test"}},
		HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodPost && req.URL.Host == "graph.microsoft.com":
				if got := req.Header.Get("Authorization"); got != "Bearer graph-access" {
					t.Fatalf("Graph authorization=%q", got)
				}
				if err := json.NewDecoder(req.Body).Decode(&graphBody); err != nil {
					t.Fatal(err)
				}
				return phase6Response(http.StatusOK, `{"uploadUrl":"https://tenant.sharepoint.com/upload/session"}`), nil
			case req.Method == http.MethodPut && req.URL.Host == "tenant.sharepoint.com":
				part, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				ranges = append(ranges, req.Header.Get("Content-Range"))
				chunks = append(chunks, part)
				count := len(chunks)
				mu.Unlock()
				if count == 1 {
					return phase6Response(http.StatusAccepted, `{"nextExpectedRanges":["983040-"]}`), nil
				}
				return phase6Response(http.StatusCreated, `{"id":"item-id","name":"ready.txt","webUrl":"https://tenant.sharepoint.com/file","spoId":"SPO_ready","parentReference":{"driveId":"drive-id"}}`), nil
			default:
				t.Fatalf("unexpected request %s %s", req.Method, req.URL)
				return nil, nil
			}
		})},
		AttachmentNameSuffix: func() (string, error) { return "fixedsuffix", nil },
	}
	attachments := []Attachment{{Type: "file", URL: dataURL, Name: "report.txt", MimeType: "text/plain"}}
	if err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat-access", GraphAccessToken: "graph-access"}, "conversation", attachments); err != nil {
		t.Fatal(err)
	}
	item, _ := graphBody["item"].(map[string]any)
	if item["@microsoft.graph.conflictBehavior"] != "replace" || item["name"] != "report-fixedsuffix.txt" {
		t.Fatalf("createUploadSession body=%#v", graphBody)
	}
	if got, want := ranges, []string{
		"bytes 0-983039/983057",
		"bytes 983040-983056/983057",
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ranges=%v want=%v", got, want)
	}
	joined := bytes.Join(chunks, nil)
	if sha256.Sum256(joined) != sha256.Sum256(raw) {
		t.Fatal("uploaded document bytes changed")
	}
	if attachments[0].OriginalName != "report.txt" || attachments[0].TransportName != "report-fixedsuffix.txt" || attachments[0].DocID != "SPO_ready" || attachments[0].ReferenceURL != "https://tenant.sharepoint.com/file" || attachments[0].UploadedConversationID != "conversation" {
		t.Fatalf("ready attachment=%#v", attachments[0])
	}

	payload := chatPayload("read it", "session", "conversation", "request", "magic", true, attachments, nil, nil, 0, "")
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.Split(payload, rs)[0]), &frame); err != nil {
		t.Fatal(err)
	}
	message := frame["arguments"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if _, exists := message["attachments"]; exists {
		t.Fatalf("raw caller attachments leaked into ChatHub payload: %#v", message["attachments"])
	}
	annotations := message["messageAnnotations"].([]any)
	annotation := annotations[0].(map[string]any)
	if annotation["messageAnnotationType"] != "LocalFile" || annotation["id"] != "SPO_ready" || annotation["text"] != "report-fixedsuffix.txt" || annotation["url"] != "https://tenant.sharepoint.com/file" {
		t.Fatalf("LocalFile annotation=%#v", annotation)
	}
	if _, exists := annotation["messageAnnotationMetadata"]; exists {
		t.Fatalf("LocalFile fabricated metadata: %#v", annotation)
	}
}

func TestWP6DocumentUploadFailureCancelsSessionAndDoesNotBecomeReady(t *testing.T) {
	var deleted bool
	client := &Client{
		HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.Method {
			case http.MethodPost:
				return phase6Response(http.StatusOK, `{"uploadUrl":"https://tenant.sharepoint.com/upload/session"}`), nil
			case http.MethodPut:
				return phase6Response(http.StatusBadGateway, `upstream failed`), nil
			case http.MethodDelete:
				deleted = true
				return phase6Response(http.StatusNoContent, ``), nil
			default:
				return nil, fmt.Errorf("unexpected method %s", req.Method)
			}
		})},
		AttachmentNameSuffix: func() (string, error) { return "fixed", nil },
	}
	attachments := []Attachment{{Type: "file", URL: "data:text/plain;base64,QQ==", Name: "a.txt"}}
	err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat", GraphAccessToken: "graph"}, "conversation", attachments)
	if err == nil || !strings.Contains(err.Error(), "document upload") {
		t.Fatalf("error=%v", err)
	}
	if !deleted {
		t.Fatal("failed upload session was not cancelled")
	}
	if attachments[0].DocID != "" || attachments[0].UploadedConversationID != "" {
		t.Fatalf("failed attachment marked ready: %#v", attachments[0])
	}
}

func TestWP6DocumentFilenameFallbackUniquenessAndUTF16Boundary(t *testing.T) {
	tests := []struct {
		name, suffix, want string
	}{
		{"file.xyz", "abc", "file-abc.xyz.txt"},
		{"file.txt", "abc", "file-abc.txt"},
		{"README", "abc", "README-abc.txt"},
	}
	for _, test := range tests {
		got, err := documentTransportName(test.name, test.suffix)
		if err != nil || got != test.want {
			t.Fatalf("documentTransportName(%q)=%q,%v want=%q", test.name, got, err, test.want)
		}
	}
	long := strings.Repeat("📎", 200) + ".xyz"
	got, err := documentTransportName(long, "unique")
	if err != nil {
		t.Fatal(err)
	}
	if utf16Units(got) > maxDocumentFilenameUTF16 || !strings.HasSuffix(got, "-unique.xyz.txt") {
		t.Fatalf("long transport name units=%d suffix=%q", utf16Units(got), got[max(0, len(got)-24):])
	}
	if got2, _ := documentTransportName("file.txt", "different"); got2 == tests[1].want {
		t.Fatal("different uploads reused a transport filename")
	}
	edge, err := documentTransportName("📎."+strings.Repeat("x", 270), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if got := utf16Units(edge); got > maxDocumentFilenameUTF16 {
		t.Fatalf("fallback transport name uses %d UTF-16 units: %q", got, edge)
	}
}

func TestWP6RemoteAttachmentPinsValidatedDNSResult(t *testing.T) {
	lookupCalls := 0
	client := &Client{
		ResolveAttachmentIPs: func(context.Context, string) ([]net.IP, error) {
			lookupCalls++
			if lookupCalls > 1 {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		PinnedHTTPSClient: func(serverName string) *http.Client {
			if serverName != "files.example" {
				t.Fatalf("TLS server name=%q", serverName)
			}
			return &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "93.184.216.34:443" || req.Host != "files.example" {
					t.Fatalf("pinned request URL host=%q Host=%q", req.URL.Host, req.Host)
				}
				return phase6Response(http.StatusOK, "exact bytes"), nil
			})}
		},
	}
	response, finalURL, err := client.downloadRemoteAttachment(context.Background(), "https://files.example/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if finalURL.Host != "files.example" || lookupCalls != 1 {
		t.Fatalf("final URL=%s lookup calls=%d", finalURL, lookupCalls)
	}
}

func TestWP6RemoteAttachmentRejectsAnyPrivateDNSAnswerBeforeDial(t *testing.T) {
	dials := 0
	client := &Client{
		ResolveAttachmentIPs: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.8")}, nil
		},
		PinnedHTTPSClient: func(string) *http.Client {
			dials++
			return &http.Client{}
		},
	}
	if _, _, err := client.downloadRemoteAttachment(context.Background(), "https://files.example/a"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error=%v", err)
	}
	if dials != 0 {
		t.Fatalf("dialed %d times after unsafe DNS answer", dials)
	}
}

func TestWP6RemoteAttachmentRejectsSpecialUseAddressesAndRedirect(t *testing.T) {
	for _, address := range []string{"0.0.0.1", "198.18.0.1", "192.0.2.1", "203.0.113.1", "240.0.0.1", "2001:db8::1", "::ffff:192.0.2.1"} {
		if !unsafeAttachmentIP(net.ParseIP(address)) {
			t.Fatalf("special-use address accepted: %s", address)
		}
	}
	dials := 0
	client := &Client{
		ResolveAttachmentIPs: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		PinnedHTTPSClient: func(string) *http.Client {
			dials++
			return &http.Client{Transport: phase6RoundTripFunc(func(*http.Request) (*http.Response, error) {
				response := phase6Response(http.StatusFound, "")
				response.Header.Set("Location", "https://192.0.2.1/private")
				return response, nil
			})}
		},
	}
	if _, _, err := client.downloadRemoteAttachment(context.Background(), "https://files.example/start"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("redirect error=%v", err)
	}
	if dials != 1 {
		t.Fatalf("special-use redirect reached a second dial: %d", dials)
	}
}

func TestWP6AttachmentQuotaAndSizeBoundaries(t *testing.T) {
	if err := validateAttachmentSize(0); err == nil {
		t.Fatal("zero-byte attachment accepted")
	}
	if err := validateAttachmentSize(maxAttachmentBytes); err != nil {
		t.Fatalf("512 MiB inclusive rejected: %v", err)
	}
	if err := validateAttachmentSize(maxAttachmentBytes + 1); err == nil {
		t.Fatal("512 MiB + 1 accepted")
	}
	client := &Client{}
	attachments := []Attachment{{Type: "image"}, {Type: "file"}, {Type: "image"}, {Type: "file"}}
	if err := client.uploadAttachments(context.Background(), Account{}, "conversation", attachments); err == nil || !strings.Contains(err.Error(), "limit is 3") {
		t.Fatalf("shared quota error=%v", err)
	}
}

func TestWP6ReadyDocumentReusesOnlySameConversation(t *testing.T) {
	requests := 0
	client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("unexpected request")
	})}}
	attachments := []Attachment{{Type: "file", URL: "data:text/plain;base64,QQ==", Name: "a.txt", DocID: "SPO_ready", TransportName: "a-unique.txt", ReferenceURL: "https://tenant.sharepoint.com/a", UploadedConversationID: "same"}}
	if err := client.uploadAttachments(context.Background(), Account{}, "same", attachments); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("ready document reuploaded %d times", requests)
	}
	if err := client.uploadAttachments(context.Background(), Account{}, "new", attachments); err == nil {
		t.Fatal("document identity from another conversation was reused")
	}
}

func TestWP6ThreeReadyMixedAttachmentsPassSharedInclusiveQuota(t *testing.T) {
	requests := 0
	client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("unexpected request")
	})}}
	attachments := []Attachment{
		{Type: "file", DocID: "SPO_one", TransportName: "one.txt", ReferenceURL: "https://tenant.sharepoint.com/one", UploadedConversationID: "same"},
		{Type: "image", DocID: "IMG_two", Name: "two.png", FileType: "png", UploadedConversationID: "same"},
		{Type: "file", DocID: "SPO_three", TransportName: "three.txt", ReferenceURL: "https://tenant.sharepoint.com/three", UploadedConversationID: "same"},
	}
	if err := client.uploadAttachments(context.Background(), Account{}, "same", attachments); err != nil {
		t.Fatalf("three active attachments rejected: %v", err)
	}
	if requests != 0 {
		t.Fatalf("ready attachments were reuploaded %d times", requests)
	}
}

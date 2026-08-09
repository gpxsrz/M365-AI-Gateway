package chathub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWP6VisionFormatsUseExactMultipartBytesAndOrderedAnnotations(t *testing.T) {
	fixtures := []struct {
		mime, fileType string
		bytes          []byte
	}{
		{"image/png", "png", []byte("\x89PNG\r\n\x1a\nPNG-BYTES")},
		{"image/jpeg", "jpeg", []byte("\xff\xd8\xffJPEG-BYTES")},
		{"image/gif", "gif", []byte("GIF89aGIF-BYTES")},
		{"image/webp", "webp", []byte("RIFF\x10\x00\x00\x00WEBPWEBP-BYTES")},
	}
	requests := 0
	client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPost || req.URL.Path != "/m365Copilot/UploadFile" {
			t.Fatalf("request=%s %s", req.Method, req.URL)
		}
		if err := req.ParseMultipartForm(2 << 20); err != nil {
			t.Fatal(err)
		}
		index := requests - 1
		if req.FormValue("scenario") != "UploadImage" || req.FormValue("conversationId") != "vision-conversation" {
			t.Fatalf("multipart metadata=%#v", req.MultipartForm.Value)
		}
		if got := req.MultipartForm.Value["optionsSets"]; fmt.Sprint(got) != fmt.Sprint([]string{"cwcgptvsan", "flux_v3_gptv_enable_upload_multi_image_in_turn_wo_ch", "gptvnorm2048"}) {
			t.Fatalf("optionsSets=%v", got)
		}
		dataURL := req.FormValue("FileBase64")
		if !strings.HasPrefix(dataURL, "data:"+fixtures[index].mime+";base64,") {
			t.Fatalf("FileBase64 prefix=%q", dataURL)
		}
		encoded := dataURL[strings.IndexByte(dataURL, ',')+1:]
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || sha256.Sum256(decoded) != sha256.Sum256(fixtures[index].bytes) {
			t.Fatalf("image bytes changed: err=%v", err)
		}
		return phase6Response(http.StatusOK, fmt.Sprintf(`{"docId":"image-%d","fileName":"image","fileType":".%s","result":{"value":"Success"}}`, index, fixtures[index].fileType)), nil
	})}}
	attachments := make([]Attachment, len(fixtures))
	for i, fixture := range fixtures {
		attachments[i] = Attachment{Type: "image", URL: "data:" + fixture.mime + ";base64," + base64.StdEncoding.EncodeToString(fixture.bytes), MimeType: fixture.mime}
	}
	// Validate the transport in two legal active turns because the shared quota
	// is three; annotation order is checked within each turn.
	for start := 0; start < len(attachments); start += 2 {
		end := min(start+2, len(attachments))
		if err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat"}, "vision-conversation", attachments[start:end]); err != nil {
			t.Fatal(err)
		}
	}
	if requests != len(fixtures) {
		t.Fatalf("upload requests=%d", requests)
	}
	if attachments[1].FileType != "jpg" {
		t.Fatalf("JPEG annotation type=%q", attachments[1].FileType)
	}
	for i := range attachments {
		if attachments[i].OriginalName != "image" {
			t.Fatalf("image %d original name=%q", i, attachments[i].OriginalName)
		}
	}
	payload := chatPayload("describe", "session", "vision-conversation", "request", "magic", true, attachments[:3], nil, nil, 0, "")
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.Split(payload, rs)[0]), &frame); err != nil {
		t.Fatal(err)
	}
	message := frame["arguments"].([]any)[0].(map[string]any)["message"].(map[string]any)
	for _, forbidden := range []string{"attachments", "imageBase64", "imageUrl", "detail", "image_detail"} {
		if _, exists := message[forbidden]; exists {
			t.Fatalf("forbidden image field %q leaked: %#v", forbidden, message)
		}
	}
	annotations := message["messageAnnotations"].([]any)
	for i, raw := range annotations {
		annotation := raw.(map[string]any)
		if annotation["id"] != fmt.Sprintf("image-%d", i) || annotation["messageAnnotationType"] != "ImageFile" {
			t.Fatalf("annotation %d=%#v", i, annotation)
		}
	}
}

func TestWP6VisionUploadFailuresAreExplicit(t *testing.T) {
	valid := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nbytes"))
	tests := []struct {
		name string
		body string
		code int
	}{
		{"status", `{}`, http.StatusBadGateway},
		{"json", `{`, http.StatusOK},
		{"missing id", `{"result":{"value":"Success"}}`, http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(*http.Request) (*http.Response, error) {
				return phase6Response(test.code, test.body), nil
			})}}
			attachments := []Attachment{{Type: "image", URL: valid}}
			if err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat"}, "conversation", attachments); err == nil {
				t.Fatal("invalid UploadFile response became success")
			}
			if attachments[0].DocID != "" || attachments[0].UploadedConversationID != "" {
				t.Fatalf("failed image marked ready: %#v", attachments[0])
			}
		})
	}
}

func TestWP6VisionRejectsInvalidBytesAndPrivateRemoteURL(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("must not issue HTTP request")
	})}}
	for _, attachment := range []Attachment{
		{Type: "image", URL: "data:image/png;base64,"},
		{Type: "image", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not png"))},
		{Type: "image", URL: "https://127.0.0.1/private.png"},
	} {
		if err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat"}, "conversation", []Attachment{attachment}); err == nil {
			t.Fatalf("invalid image accepted: %#v", attachment)
		}
	}
}

func TestWP6VisionMultipartReadErrorPropagates(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, req.Body)
		return nil, fmt.Errorf("network failure")
	})}}
	image := []byte("\x89PNG\r\n\x1a\nbytes")
	err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat"}, "conversation", []Attachment{{Type: "image", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)}})
	if err == nil || strings.Contains(err.Error(), "conversation") {
		t.Fatalf("safe upload error=%v", err)
	}
}

func TestWP6VisionOver2048PixelsIsNotResizedOrReencoded(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2049, 2))); err != nil {
		t.Fatal(err)
	}
	want := encoded.Bytes()
	config, err := png.DecodeConfig(bytes.NewReader(want))
	if err != nil || config.Width != 2049 {
		t.Fatalf("fixture dimensions=%dx%d err=%v", config.Width, config.Height, err)
	}
	client := &Client{HTTPClient: &http.Client{Transport: phase6RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatal(err)
		}
		dataURL := req.FormValue("FileBase64")
		comma := strings.IndexByte(dataURL, ',')
		if comma < 0 {
			t.Fatalf("missing data URL: %q", dataURL)
		}
		got, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
		if err != nil || sha256.Sum256(got) != sha256.Sum256(want) {
			t.Fatalf("large image bytes changed: size=%d err=%v", len(got), err)
		}
		return phase6Response(http.StatusOK, `{"docId":"large","fileName":"large.png","fileType":".png","result":{"value":"Success"}}`), nil
	})}}
	attachment := Attachment{Type: "image", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(want), MimeType: "image/png"}
	if err := client.uploadAttachments(context.Background(), Account{AccessToken: "chat"}, "conversation", []Attachment{attachment}); err != nil {
		t.Fatal(err)
	}
}

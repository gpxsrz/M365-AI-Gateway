package chathub

import (
	"encoding/json"
	"testing"
)

func TestImageURLs(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"content":{"image":{"downloadUrl":"https://cdn.example.com/image/1.png","thumbnailUrl":"https://cdn.example.com/image/1.png"}},"url":"https://example.com/page"}`), json.RawMessage(`{"src":"https://cdn.example.com/image/2.webp"}`)}
	got := imageURLs(raw)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestImageURLsRejectsUnsafe(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"url":"http://example.com/a.png"}`)}
	if got := imageURLs(raw); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestIsImageURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "https image path", url: "https://cdn.example.com/image/1.png", want: true},
		{name: "valid data image", url: "data:image/png;base64,V1Ax", want: true},
		{name: "http rejected", url: "http://cdn.example.com/image/1.png"},
		{name: "non-image https rejected", url: "https://example.com/page"},
		{name: "missing data comma rejected", url: "data:image/png;base64"},
		{name: "invalid base64 rejected", url: "data:image/png;base64,%%%"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsImageURL(tc.url); got != tc.want {
				t.Fatalf("IsImageURL(%q)=%t want=%t", tc.url, got, tc.want)
			}
		})
	}
}

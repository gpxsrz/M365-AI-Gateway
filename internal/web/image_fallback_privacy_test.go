package web

import (
	"reflect"
	"testing"
)

func TestImageFallbackRejectsProtectedArtifactURLs(t *testing.T) {
	raw := `{"url":"https://artifact.asyncgw.teams.microsoft.com/v1/objects/id/views/original/private-image.png","imageUrl":"https://cdn.example.test/safe-image.png"}`
	got := extractImageURLs(raw)
	want := []string{"https://cdn.example.test/safe-image.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractImageURLs()=%#v want %#v", got, want)
	}
}

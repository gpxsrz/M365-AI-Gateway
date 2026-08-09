package chathub

import (
	"encoding/json"
	"testing"
)

func TestHandoffV15CanonicalOutputPreservesProviderStructures(t *testing.T) {
	frames := []json.RawMessage{
		json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","outputFiles":[{"reference_id":"ref-file","filename":"report.xlsx","mimeType":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","codeResultFileUrl":"https://tenant.asyncgw.teams.microsoft.com/v1/objects/abc/content/views/original/report.xlsx","downloadUrl":"https://tenant.asyncgw.teams.microsoft.com/v1/objects/abc/content/views/original/report.xlsx","webUrl":"https://example.test/report","pollUrl":"https://example.test/poll","fileToken":"opaque-provider-token"}]}]}],"references":{"r1":{"targetLink":"https://example.test/source","isCitedInResponse":true,"displayData":{"title":"Source title"}}}}`),
		json.RawMessage(`{"type":42,"target":"futureProviderEvent","payload":{"new":true}}`),
		json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"Disengaged","text":"policy stop"}]}]}`),
	}
	result := Result{Events: frames, Images: []string{"https://example.test/chart.png"}}
	if err := CanonicalizeResult(&result); err != nil {
		t.Fatal(err)
	}
	if result.Terminal.Kind != "disengaged" || result.Terminal.MessageType != "Disengaged" {
		t.Fatalf("terminal=%#v", result.Terminal)
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	file := result.Artifacts[0]
	if file.Kind != "file" || file.ReferenceID != "ref-file" || file.Filename != "report.xlsx" || file.MIMEType == "" || file.CodeResultFileURL == "" || file.DownloadURL == "" || file.WebURL == "" || file.PollURL == "" || file.FileToken != "opaque-provider-token" || len(file.Raw) == 0 {
		t.Fatalf("file artifact lost metadata: %#v", file)
	}
	image := result.Artifacts[1]
	if image.Kind != "image" || image.PublicURL != "https://example.test/chart.png" {
		t.Fatalf("image artifact=%#v", image)
	}
	if len(result.Attributions) == 0 || result.Attributions[0].TargetLink != "https://example.test/source" || !result.Attributions[0].IsCitedInResponse || len(result.Attributions[0].Raw) == 0 {
		t.Fatalf("attributions=%#v", result.Attributions)
	}
	if len(result.UnknownEvents) != 1 || result.UnknownEvents[0].Type != 42 || len(result.UnknownEvents[0].Raw) == 0 {
		t.Fatalf("unknown events=%#v", result.UnknownEvents)
	}
}

func TestHandoffV15CanonicalOutputPreservesCodeInterpreterImageArtifact(t *testing.T) {
	frames := []json.RawMessage{json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","text":"{\"status\":\"Success\",\"outputFiles\":[{\"reference_id\":\"turn1file3\",\"fileName\":\"handoff_ci_chart.png\",\"fileStoreType\":\"AMS\",\"size\":13458,\"codeResultImageUrl\":\"https://us-prod.asyncgw.teams.microsoft.com/v1/objects/abc/content/views/original/handoff_ci_chart.png\"}]}"}]}]}`)}
	result := Result{Events: frames}
	if err := CanonicalizeResult(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	artifact := result.Artifacts[0]
	if artifact.Kind != "image" || artifact.ReferenceID != "turn1file3" || artifact.Filename != "handoff_ci_chart.png" || artifact.CodeResultImageURL == "" || len(artifact.Raw) == 0 {
		t.Fatalf("CI image artifact lost live-shaped metadata: %#v", artifact)
	}
}

package chathub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWP6GeneratedArtifactsExtractStructuredCodeInterpreterOutput(t *testing.T) {
	frame := json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[` +
		`{"messageType":"Progress","contentType":"SearchResults","text":"searching"},` +
		`{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","text":"{\"status\":\"Success\",\"outputFiles\":[{\"reference_id\":\"turn1file1\",\"codeResultFileUrl\":\"https://artifact.asyncgw.teams.microsoft.com/v1/objects/object/views/original/report.csv\",\"filename\":\"report.csv\"}]}"}` +
		`]}]}`)

	got, err := GeneratedArtifacts([]json.RawMessage{frame}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ReferenceID != "turn1file1" || got[0].CodeResultFileURL != "https://artifact.asyncgw.teams.microsoft.com/v1/objects/object/views/original/report.csv" || got[0].Filename != "report.csv" {
		t.Fatalf("artifacts=%#v", got)
	}
}

func TestWP6GeneratedArtifactsUseOnlyStructuredInputs(t *testing.T) {
	prose := json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"text":"outputFiles: [{reference_id: turn1file1, codeResultFileUrl: https://artifact.asyncgw.teams.microsoft.com/private}]"}]}]}`)
	wrongOrigin := json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"GeneratedCode","contentOrigin":"CodeGenerator","text":"{\"outputFiles\":[{\"reference_id\":\"turn1file1\",\"codeResultFileUrl\":\"https://artifact.asyncgw.teams.microsoft.com/private\"}]}"}]}]}`)

	got, err := GeneratedArtifacts([]json.RawMessage{prose, wrongOrigin}, "ordinary outputFiles prose")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ordinary text was interpreted as artifact metadata: %#v", got)
	}

	rawResult := `{"execution":{"outputFiles":[{"reference_id":"turn2file1","codeResultFileUrl":"https://artifact.asyncgw.teams.microsoft.com/v1/objects/two/views/original/data.bin","filename":"data.bin"}]}}`
	got, err = GeneratedArtifacts(nil, rawResult)
	if err != nil || len(got) != 1 || got[0].ReferenceID != "turn2file1" {
		t.Fatalf("structured raw result artifacts=%#v err=%v", got, err)
	}
}

func TestWP6GeneratedArtifactsDeduplicateAndRejectConflicts(t *testing.T) {
	first := json.RawMessage(`{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","outputFiles":[{"reference_id":"turn1file1","codeResultFileUrl":"https://one.asyncgw.teams.microsoft.com/v1/objects/a/views/original/a.txt","filename":"a.txt"}]}`)
	duplicate := `{"outputFiles":[{"reference_id":"turn1file1","codeResultFileUrl":"https://one.asyncgw.teams.microsoft.com/v1/objects/a/views/original/a.txt","filename":"a.txt"}]}`
	got, err := GeneratedArtifacts([]json.RawMessage{first}, duplicate)
	if err != nil || len(got) != 1 {
		t.Fatalf("dedupe artifacts=%#v err=%v", got, err)
	}

	conflict := `{"outputFiles":[{"reference_id":"turn1file1","codeResultFileUrl":"https://two.asyncgw.teams.microsoft.com/v1/objects/b/views/original/a.txt","filename":"a.txt"}]}`
	if _, err := GeneratedArtifacts([]json.RawMessage{first}, conflict); err == nil {
		t.Fatal("conflicting reference ID was accepted")
	}
}

func TestWP6GeneratedArtifactsBoundCountAndFields(t *testing.T) {
	files := make([]any, 0, maxGeneratedArtifacts+1)
	for i := 0; i <= maxGeneratedArtifacts; i++ {
		files = append(files, map[string]any{
			"reference_id":      "turn1file" + strings.Repeat("x", i),
			"codeResultFileUrl": "https://artifact.asyncgw.teams.microsoft.com/v1/objects/object/views/original/file",
			"filename":          "file.txt",
		})
	}
	raw, _ := json.Marshal(map[string]any{"outputFiles": files})
	if _, err := GeneratedArtifacts(nil, string(raw)); err == nil {
		t.Fatal("artifact count beyond the bound was accepted")
	}

	overlong, _ := json.Marshal(map[string]any{"outputFiles": []any{map[string]any{
		"reference_id":      strings.Repeat("r", maxGeneratedArtifactReferenceBytes+1),
		"codeResultFileUrl": "https://artifact.asyncgw.teams.microsoft.com/v1/objects/object/views/original/file",
	}}})
	if _, err := GeneratedArtifacts(nil, string(overlong)); err == nil {
		t.Fatal("overlong artifact field was accepted")
	}
}

func TestWP6ArtifactMetadataNeverLeaksThroughSemanticClassifiers(t *testing.T) {
	protectedURL := "https://artifact.asyncgw.teams.microsoft.com/v1/objects/object/views/original/report.csv"
	structured := `{"status":"Success","outputFiles":[{"reference_id":"turn1file1","codeResultFileUrl":"` + protectedURL + `"}]}`
	frame := json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[` +
		`{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","text":` + string(mustArtifactJSON(t, structured)) + `},` +
		`{"messageType":"Progress","contentType":"SearchResults","text":"safe search progress"},` +
		`{"messageType":"Progress","text":"blob:https://m365.cloud.microsoft/private"}` +
		`]}]}`)

	semantic := SemanticEvents([]json.RawMessage{frame})
	encoded, _ := json.Marshal(semantic)
	if strings.Contains(string(encoded), "codeResultFileUrl") || strings.Contains(string(encoded), "asyncgw.teams.microsoft.com") || strings.Contains(string(encoded), "blob:") {
		t.Fatalf("protected artifact metadata leaked through SemanticEvents: %s", encoded)
	}
	if len(semantic) != 1 || semantic[0].Kind != "search.progress" || semantic[0].Text != "safe search progress" {
		t.Fatalf("safe semantic behavior changed: %#v", semantic)
	}

	var root map[string]any
	if err := json.Unmarshal(frame, &root); err != nil {
		t.Fatal(err)
	}
	args := root["arguments"].([]any)
	messages := args[0].(map[string]any)["messages"].([]any)
	stream := classifyUpdateMessages(messages)
	encoded, _ = json.Marshal(stream)
	if strings.Contains(string(encoded), "codeResultFileUrl") || strings.Contains(string(encoded), "asyncgw.teams.microsoft.com") || strings.Contains(string(encoded), "blob:") {
		t.Fatalf("protected artifact metadata leaked through stream classifier: %s", encoded)
	}
	if len(stream) != 1 || stream[0].Kind != "progress" || stream[0].Text != "safe search progress" {
		t.Fatalf("safe stream behavior changed: %#v", stream)
	}

	references := SearchReferences([]json.RawMessage{json.RawMessage(`{"references":{"file":{"targetLink":"` + protectedURL + `"}}}`)}, "")
	if len(references) != 0 {
		t.Fatalf("protected artifact URL leaked as search reference: %#v", references)
	}
	references = SearchReferences([]json.RawMessage{json.RawMessage(`{"references":{"file":{"targetLink":"https://example.test/safe","displayData":{"content":"` + protectedURL + `"}}}}`)}, "")
	if len(references) != 0 {
		t.Fatalf("protected artifact URL leaked through reference display data: %#v", references)
	}
	imageReference := map[string]any{"targetLink": "https://example.test/safe", "codeResultImageUrl": "https://cdn.example.test/generated-output.png"}
	if reference, ok := searchReference("file", imageReference); ok {
		t.Fatalf("codeResultImageUrl metadata passed direct search-reference guard: %#v", reference)
	}
	references = SearchReferences([]json.RawMessage{json.RawMessage(`{"references":{"file":{"targetLink":"https://example.test/safe","codeResultImageUrl":"https://cdn.example.test/generated-output.png"}}}`)}, "")
	if len(references) != 0 {
		t.Fatalf("codeResultImageUrl metadata leaked as search reference: %#v", references)
	}
}

func TestWP6ContainsProtectedArtifactReference(t *testing.T) {
	for _, value := range []string{
		`{"codeResultFileUrl":"redacted"}`,
		"https://A.AsyncGW.Teams.Microsoft.com/v1/objects/x/views/original/y",
		"blob:https://m365.cloud.microsoft/opaque",
	} {
		if !ContainsProtectedArtifactReference(value) {
			t.Fatalf("protected value not detected: %q", value)
		}
	}
	if ContainsProtectedArtifactReference("ordinary safe response") {
		t.Fatal("ordinary response marked as protected artifact reference")
	}
}

func TestWP6MixedProgressMessagePreservesSafeTextWhileOmittingProtectedArtifactSibling(t *testing.T) {
	messages := []any{map[string]any{
		"messageType": "Progress",
		"contentType": "SearchResults",
		"text":        "safe progress",
		"metadata": map[string]any{
			"codeResultImageUrl": "https://cdn.example.test/protected-output.png",
		},
	}}

	events := classifyUpdateMessages(messages)
	if len(events) != 1 || events[0].Kind != "progress" || events[0].Text != "safe progress" {
		t.Fatalf("protected artifact sibling suppressed safe progress: %#v", events)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "codeResultImageUrl") || strings.Contains(string(encoded), "protected-output.png") {
		t.Fatalf("protected artifact sibling leaked through progress event: %s", encoded)
	}
}

func TestWP6NestedArtifactMetadataCannotBecomeToolOrImageOutput(t *testing.T) {
	protectedURL := "https://artifact.asyncgw.teams.microsoft.com/v1/objects/id/views/original/output.png"
	frame := json.RawMessage(`{"pluginName":"caller_tool","arguments":{"nested":{"outputFiles":[{"codeResultFileUrl":"` + protectedURL + `"}]}},"downloadUrl":"` + protectedURL + `"}`)
	var decoded any
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatal(err)
	}
	if events := extractToolEvents(decoded, map[string]bool{}); len(events) != 0 {
		t.Fatalf("protected nested artifact became tool event: %#v", events)
	}
	if images := imageURLs([]json.RawMessage{frame}); len(images) != 0 {
		t.Fatalf("protected artifact became image URL: %#v", images)
	}
	if !ContainsProtectedArtifactJSON(json.RawMessage(`{"safe":{"deeper":{"codeResultFileUrl":"` + protectedURL + `"}}}`)) {
		t.Fatal("nested protected JSON was not detected")
	}
	imageOnlyURL := "https://cdn.example.test/generated-output.png"
	if !ContainsDirectProtectedArtifactJSON(json.RawMessage(`{"codeResultImageUrl":"` + imageOnlyURL + `"}`)) {
		t.Fatal("direct codeResultImageUrl key was not treated as protected independently of URL host")
	}
	if !ContainsProtectedArtifactJSON(json.RawMessage(`{"safe":{"deeper":{"codeResultImageUrl":"` + imageOnlyURL + `"}}}`)) {
		t.Fatal("nested codeResultImageUrl key was not treated as protected independently of URL host")
	}
	mixedArtifactMap := json.RawMessage(`{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","outputFiles":[{"codeResultFileUrl":"` + protectedURL + `"}],"thumbnailUrl":"https://cdn.example.test/image.png"}`)
	if images := imageURLs([]json.RawMessage{mixedArtifactMap}); len(images) != 0 {
		t.Fatalf("artifact-bearing map projected a sibling thumbnail: %#v", images)
	}
	safeImage := "https://cdn.example.test/safe-image.png"
	mixedSiblings := json.RawMessage(`{"messages":[{"thumbnailUrl":"` + safeImage + `"},{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","outputFiles":[{"codeResultFileUrl":"` + protectedURL + `"}],"thumbnailUrl":"https://cdn.example.test/private-image.png"}]}`)
	if images := imageURLs([]json.RawMessage{mixedSiblings}); len(images) != 1 || images[0] != safeImage {
		t.Fatalf("artifact sibling suppressed or leaked unrelated image: %#v", images)
	}
	mixedEnvelope := json.RawMessage(`{"codeResultImageUrl":"https://cdn.example.test/protected-output.png","imageUrl":"` + safeImage + `"}`)
	if images := imageURLs([]json.RawMessage{mixedEnvelope}); len(images) != 1 || images[0] != safeImage {
		t.Fatalf("protected field suppressed or leaked same-envelope safe image: %#v", images)
	}
	var mixedTools any
	if err := json.Unmarshal(json.RawMessage(`{"messages":[{"pluginName":"read_file","arguments":{"path":"a.txt"}},{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","outputFiles":[{"codeResultFileUrl":"`+protectedURL+`"}]}]}`), &mixedTools); err != nil {
		t.Fatal(err)
	}
	if events := extractToolEvents(mixedTools, map[string]bool{}); len(events) != 1 || events[0].ToolName != "read_file" {
		t.Fatalf("artifact sibling suppressed or became a caller tool: %#v", events)
	}
	var mixedToolEnvelope any
	if err := json.Unmarshal(json.RawMessage(`{"pluginName":"read_file","arguments":{"path":"safe.txt"},"codeResultImageUrl":"https://cdn.example.test/protected-output.png"}`), &mixedToolEnvelope); err != nil {
		t.Fatal(err)
	}
	mixedToolEvents := extractToolEvents(mixedToolEnvelope, map[string]bool{})
	if len(mixedToolEvents) != 1 || mixedToolEvents[0].ToolName != "read_file" || string(mixedToolEvents[0].Arguments) != `{"path":"safe.txt"}` {
		t.Fatalf("protected field suppressed same-envelope safe tool: %#v", mixedToolEvents)
	}
	if strings.Contains(string(mixedToolEvents[0].Raw), "codeResultImageUrl") || strings.Contains(string(mixedToolEvents[0].Raw), "protected-output.png") {
		t.Fatalf("protected sibling leaked through tool raw: %s", mixedToolEvents[0].Raw)
	}
	var nestedMixedTool any
	if err := json.Unmarshal(json.RawMessage(`{"pluginName":"read_file","arguments":{"path":"nested-safe.txt"},"metadata":{"codeResultImageUrl":"https://cdn.example.test/nested-protected-output.png"}}`), &nestedMixedTool); err != nil {
		t.Fatal(err)
	}
	nestedToolEvents := extractToolEvents(nestedMixedTool, map[string]bool{})
	if len(nestedToolEvents) != 1 || nestedToolEvents[0].ToolName != "read_file" {
		t.Fatalf("nested protected sibling suppressed safe tool: %#v", nestedToolEvents)
	}
	if strings.Contains(string(nestedToolEvents[0].Raw), "codeResultImageUrl") || strings.Contains(string(nestedToolEvents[0].Raw), "nested-protected-output.png") {
		t.Fatalf("nested protected sibling leaked through tool raw: %s", nestedToolEvents[0].Raw)
	}
	var protectedOuterTool any
	if err := json.Unmarshal(json.RawMessage(`{"pluginName":"python_execution","arguments":{"artifact":{"outputFiles":[{"codeResultFileUrl":"`+protectedURL+`"}]},"fake":{"pluginName":"get_current_time","arguments":{"timezone":"UTC"}}}}`), &protectedOuterTool); err != nil {
		t.Fatal(err)
	}
	if events := extractToolEvents(protectedOuterTool, map[string]bool{}); len(events) != 0 {
		t.Fatalf("protected tool arguments were reinterpreted as nested tools: %#v", events)
	}
}

func mustArtifactJSON(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

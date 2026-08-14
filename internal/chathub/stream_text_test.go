package chathub

import "testing"

func TestFoldStreamText(t *testing.T) {
	tests := []struct {
		name                string
		current, snapshot   string
		wantNext, wantDelta string
	}{
		{name: "first snapshot", snapshot: "hello", wantNext: "hello", wantDelta: "hello"},
		{name: "growing snapshot", current: "hello", snapshot: "hello world", wantNext: "hello world", wantDelta: " world"},
		{name: "older snapshot", current: "hello world", snapshot: "hello", wantNext: "hello world"},
		{name: "duplicate snapshot", current: "hello", snapshot: "hello", wantNext: "hello"},
		{name: "divergent snapshot", current: "hello", snapshot: "goodbye", wantNext: "hello"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, delta := foldStreamText(tc.current, tc.snapshot, true)
			if next != tc.wantNext || delta != tc.wantDelta {
				t.Fatalf("next=%q delta=%q want_next=%q want_delta=%q", next, delta, tc.wantNext, tc.wantDelta)
			}
		})
	}
}

func TestFoldStreamTextSequenceDoesNotReemitSnapshots(t *testing.T) {
	current := ""
	emitted := ""
	for _, snapshot := range []string{"A", "AB", "AB", "ABC", "AB", "XYZ"} {
		next, delta := foldStreamText(current, snapshot, true)
		current = next
		emitted += delta
	}
	if current != "ABC" || emitted != "ABC" {
		t.Fatalf("current=%q emitted=%q", current, emitted)
	}
}

func TestFoldStreamTextAppendsCursorUpdates(t *testing.T) {
	current, delta := foldStreamText("hello", " world", false)
	if current != "hello world" || delta != " world" {
		t.Fatalf("current=%q delta=%q", current, delta)
	}
	next, duplicate := foldStreamText(current, "hello world", true)
	if next != current || duplicate != "" {
		t.Fatalf("next=%q duplicate=%q", next, duplicate)
	}
}

func TestReconcileCompletionTextPrefersProvablyMoreCompletePrefix(t *testing.T) {
	tests := []struct {
		name         string
		final        string
		streamed     string
		wantText     string
		wantRelation string
		wantSource   string
	}{
		{name: "equal", final: "complete", streamed: "complete", wantText: "complete", wantRelation: "equal", wantSource: "final"},
		{name: "short final prefix", final: `{"calls":[`, streamed: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`, wantText: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`, wantRelation: "final_prefix_of_stream", wantSource: "stream"},
		{name: "short stream prefix", final: "complete answer", streamed: "complete", wantText: "complete answer", wantRelation: "stream_prefix_of_final", wantSource: "final"},
		{name: "divergent preserves final", final: "final snapshot", streamed: "different streamed text with more bytes", wantText: "final snapshot", wantRelation: "divergent", wantSource: "final"},
		{name: "final only", final: "final", streamed: "", wantText: "final", wantRelation: "final_only", wantSource: "final"},
		{name: "stream only", final: "", streamed: "stream", wantText: "stream", wantRelation: "stream_only", wantSource: "stream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotRelation, gotSource := reconcileCompletionText(tc.final, tc.streamed)
			if gotText != tc.wantText || gotRelation != tc.wantRelation || gotSource != tc.wantSource {
				t.Fatalf("reconcileCompletionText(%q,%q)=(%q,%q,%q), want (%q,%q,%q)", tc.final, tc.streamed, gotText, gotRelation, gotSource, tc.wantText, tc.wantRelation, tc.wantSource)
			}
		})
	}
}

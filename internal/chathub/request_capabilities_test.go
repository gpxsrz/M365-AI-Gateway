package chathub

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIssue67RequestCapabilityBaselineMatchesChatPayload(t *testing.T) {
	baseline := CurrentRequestCapabilityBaseline()
	payload := chatPayload("hello", "session", "conversation", "request", "Magic", true, nil, nil, nil, 1, "")
	parts := strings.Split(payload, rs)
	if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
		t.Fatalf("payload=%q", payload)
	}
	var frame struct {
		Arguments []struct {
			StreamingMode       string   `json:"streamingMode"`
			OptionsSets         []string `json:"optionsSets"`
			AllowedMessageTypes []string `json:"allowedMessageTypes"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(parts[0]), &frame); err != nil {
		t.Fatal(err)
	}
	if len(frame.Arguments) != 1 {
		t.Fatalf("arguments=%d", len(frame.Arguments))
	}
	argument := frame.Arguments[0]
	if argument.StreamingMode != baseline.StreamingMode || !reflect.DeepEqual(argument.OptionsSets, baseline.OptionsSets) || !reflect.DeepEqual(argument.AllowedMessageTypes, baseline.AllowedMessageTypes) {
		t.Fatalf("payload capability baseline drifted: argument=%#v baseline=%#v", argument, baseline)
	}
}

func TestIssue67RequestCapabilityBaselineReturnsDefensiveCopies(t *testing.T) {
	first := CurrentRequestCapabilityBaseline()
	first.OptionsSets[0] = "mutated"
	first.AllowedMessageTypes[0] = "mutated"
	second := CurrentRequestCapabilityBaseline()
	if second.OptionsSets[0] == "mutated" || second.AllowedMessageTypes[0] == "mutated" {
		t.Fatalf("baseline slices were shared: %#v", second)
	}
}

package chathub

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type Event struct {
	Type           int             `json:"type,omitempty"`
	Target         string          `json:"target,omitempty"`
	Invocation     string          `json:"invocationId,omitempty"`
	Kind           string          `json:"kind"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	Item           json.RawMessage `json:"item,omitempty"`
	Error          json.RawMessage `json:"error,omitempty"`
	ErrorText      string          `json:"errorText,omitempty"`
	AllowReconnect *bool           `json:"allowReconnect,omitempty"`
	Raw            json.RawMessage `json:"raw"`
}

type TerminalState struct {
	Kind           string `json:"kind"`
	Error          string `json:"error,omitempty"`
	AllowReconnect *bool  `json:"allowReconnect,omitempty"`
	MessageType    string `json:"messageType,omitempty"`
}

type TerminalError struct {
	State TerminalState
}

func (e *TerminalError) Error() string {
	if e == nil {
		return "ChatHub terminal error"
	}
	parts := []string{"chathub terminal " + e.State.Kind}
	if e.State.Error != "" {
		parts = append(parts, e.State.Error)
	}
	if e.State.AllowReconnect != nil {
		parts = append(parts, fmt.Sprintf("allowReconnect=%t", *e.State.AllowReconnect))
	}
	return strings.Join(parts, ": ")
}

// SemanticEvent exposes tool-like M365 progress without discarding the native frame.
type SemanticEvent struct {
	Kind                string   `json:"kind"`
	ContentType         string   `json:"contentType,omitempty"`
	MessageType         string   `json:"messageType,omitempty"`
	ContentOrigin       string   `json:"contentOrigin,omitempty"`
	AddToChainOfThought bool     `json:"addToChainOfThought,omitempty"`
	Text                string   `json:"text,omitempty"`
	Queries             []string `json:"queries,omitempty"`
}

type SearchReference struct {
	ID                string         `json:"id"`
	TargetLink        string         `json:"targetLink"`
	IsCitedInResponse bool           `json:"isCitedInResponse"`
	DisplayData       map[string]any `json:"displayData,omitempty"`
}

const maxSearchReferences = 64

func normalize(raw json.RawMessage) Event {
	var x struct {
		Type           int             `json:"type"`
		Target         string          `json:"target"`
		Invocation     string          `json:"invocationId"`
		Arguments      json.RawMessage `json:"arguments"`
		Item           json.RawMessage `json:"item"`
		Error          json.RawMessage `json:"error"`
		AllowReconnect *bool           `json:"allowReconnect"`
	}
	_ = json.Unmarshal(raw, &x)
	errorText := providerErrorText(x.Error)
	kind := "unknown"
	switch {
	case x.Type == 6:
		kind = "ping"
	case x.Type == 1 && x.Target == "update":
		kind = "update"
	case x.Type == 2:
		kind = "result"
	case x.Type == 3 && errorText != "":
		kind = "error"
	case x.Type == 3:
		kind = "complete"
	case x.Type == 7:
		kind = "close"
	case x.Target != "":
		kind = "target"
	}
	return Event{Type: x.Type, Target: x.Target, Invocation: x.Invocation, Kind: kind, Arguments: x.Arguments, Item: x.Item, Error: x.Error, ErrorText: errorText, AllowReconnect: x.AllowReconnect, Raw: append(json.RawMessage(nil), raw...)}
}

func providerErrorText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			return string(encoded)
		}
	}
	return strings.TrimSpace(string(raw))
}

func NormalizeEvents(raw []json.RawMessage) []Event {
	out := make([]Event, 0, len(raw))
	for _, r := range raw {
		out = append(out, normalize(r))
	}
	return out
}

func terminalStateFromEvents(raw []json.RawMessage, fallback TerminalState) TerminalState {
	state := fallback
	for _, frame := range raw {
		event := normalize(frame)
		switch event.Kind {
		case "error":
			state = TerminalState{Kind: "error", Error: event.ErrorText}
		case "close":
			state = TerminalState{Kind: "close", Error: event.ErrorText, AllowReconnect: event.AllowReconnect}
		}
		if event.Kind != "update" {
			continue
		}
		var arguments []struct {
			Messages []struct {
				MessageType string `json:"messageType"`
				Text        string `json:"text"`
			} `json:"messages"`
		}
		if json.Unmarshal(event.Arguments, &arguments) != nil {
			continue
		}
		for _, argument := range arguments {
			for _, message := range argument.Messages {
				switch message.MessageType {
				case "AuthError":
					state = TerminalState{Kind: "auth_error", Error: strings.TrimSpace(message.Text), MessageType: message.MessageType}
				case "Disengaged":
					if state.Kind != "auth_error" {
						state = TerminalState{Kind: "disengaged", Error: strings.TrimSpace(message.Text), MessageType: message.MessageType}
					}
				case "EndOfRequest":
					if state.Kind == "" || state.Kind == "complete" || state.Kind == "end_of_request" {
						state = TerminalState{Kind: "end_of_request", MessageType: message.MessageType}
					}
				}
			}
		}
	}
	if state.Kind == "" {
		state.Kind = "complete"
	}
	return state
}

func SemanticEvents(raw []json.RawMessage) []SemanticEvent {
	var out []SemanticEvent
	for _, r := range raw {
		e := normalize(r)
		if e.Kind != "update" {
			continue
		}
		var a []struct {
			Messages []struct {
				Text                string   `json:"text"`
				ContentType         string   `json:"contentType"`
				MessageType         string   `json:"messageType"`
				ContentOrigin       string   `json:"contentOrigin"`
				AddToChainOfThought bool     `json:"addToChainOfThought"`
				SearchQueries       []string `json:"searchQueries"`
			} `json:"messages"`
		}
		if json.Unmarshal(e.Arguments, &a) != nil {
			continue
		}
		for _, arg := range a {
			for _, m := range arg.Messages {
				if artifactBearingMessage(map[string]any{"messageType": m.MessageType, "contentOrigin": m.ContentOrigin}, m.Text, m.SearchQueries) {
					continue
				}
				kind := "message"
				if reasoningSummaryMessage(m.MessageType, m.ContentOrigin, m.AddToChainOfThought, m.Text) {
					kind = "reasoning.summary"
				} else {
					switch m.ContentType {
					case "SearchResults":
						kind = "search.progress"
					case "Code":
						kind = "code.progress"
					}
					if m.MessageType == "Progress" && kind == "message" {
						kind = "tool.progress"
					}
				}
				out = append(out, SemanticEvent{Kind: kind, ContentType: m.ContentType, MessageType: m.MessageType, ContentOrigin: m.ContentOrigin, AddToChainOfThought: m.AddToChainOfThought, Text: m.Text, Queries: m.SearchQueries})
			}
		}
	}
	return out
}

func reasoningSummaryMessage(messageType, contentOrigin string, addToChainOfThought bool, text string) bool {
	return messageType == "Progress" && strings.TrimSpace(text) != "" && (contentOrigin == "ChainOfThoughtSummary" || addToChainOfThought)
}

func ReasoningSummaries(raw []json.RawMessage) []string {
	var summaries []string
	seen := map[string]struct{}{}
	for _, event := range SemanticEvents(raw) {
		if event.Kind != "reasoning.summary" {
			continue
		}
		if _, duplicate := seen[event.Text]; duplicate {
			continue
		}
		seen[event.Text] = struct{}{}
		summaries = append(summaries, event.Text)
	}
	return summaries
}

func ReasoningContent(raw []json.RawMessage) string {
	return strings.Join(ReasoningSummaries(raw), "\n")
}

func SearchReferences(raw []json.RawMessage, rawResult string) []SearchReference {
	byID := map[string]SearchReference{}
	for _, frame := range raw {
		var value any
		if json.Unmarshal(frame, &value) == nil {
			collectSearchReferences(value, byID)
		}
	}
	if strings.TrimSpace(rawResult) != "" {
		var value any
		if json.Unmarshal([]byte(rawResult), &value) == nil {
			collectSearchReferences(value, byID)
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > maxSearchReferences {
		ids = ids[:maxSearchReferences]
	}
	out := make([]SearchReference, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

func collectSearchReferences(value any, out map[string]SearchReference) {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			collectSearchReferences(child, out)
		}
	case map[string]any:
		if references, ok := node["references"].(map[string]any); ok {
			for id, rawReference := range references {
				if reference, ok := searchReference(id, rawReference); ok {
					putBoundedSearchReference(out, reference)
				}
			}
		}
		for _, child := range node {
			collectSearchReferences(child, out)
		}
	}
}

func putBoundedSearchReference(out map[string]SearchReference, reference SearchReference) {
	if _, exists := out[reference.ID]; exists || len(out) < maxSearchReferences {
		out[reference.ID] = reference
		return
	}
	maxID := ""
	for id := range out {
		if id > maxID {
			maxID = id
		}
	}
	if reference.ID < maxID {
		delete(out, maxID)
		out[reference.ID] = reference
	}
}

func searchReference(id string, value any) (SearchReference, bool) {
	reference, ok := value.(map[string]any)
	if !ok || strings.TrimSpace(id) == "" || len(id) > 512 || artifactReference(reference) {
		return SearchReference{}, false
	}
	target, _ := reference["targetLink"].(string)
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || ContainsProtectedArtifactReference(target) {
		return SearchReference{}, false
	}
	display := map[string]any{}
	if rawDisplay, ok := reference["displayData"].(map[string]any); ok {
		for _, key := range []string{"type", "renderType", "content"} {
			if text, ok := rawDisplay[key].(string); ok && text != "" {
				if ContainsProtectedArtifactReference(text) {
					return SearchReference{}, false
				}
				display[key] = boundedReferenceText(text)
			}
		}
	}
	cited, _ := reference["isCitedInResponse"].(bool)
	return SearchReference{ID: id, TargetLink: parsed.String(), IsCitedInResponse: cited, DisplayData: display}, true
}

func artifactReference(value any) bool {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if artifactReference(child) {
				return true
			}
		}
	case map[string]any:
		for key, child := range node {
			switch strings.ToLower(key) {
			case "filestoretype", "referenceid", "citationrefid", "coderesultfileurl", "coderesultimageurl":
				return true
			case "pluginname", "providerdisplayname":
				if text, ok := child.(string); ok && (strings.EqualFold(text, "python_execution") || strings.EqualFold(text, "CodeInterpreter")) {
					return true
				}
			}
			if artifactReference(child) {
				return true
			}
		}
	}
	return false
}

func boundedReferenceText(value string) string {
	const limit = 4096
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

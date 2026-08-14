package web

import (
	"encoding/json"
	"sort"
	"strings"
)

type protocolIngressItemEvidence struct {
	Raw                 json.RawMessage
	Type                string
	Extensions          map[string]json.RawMessage
	ContentRaw          json.RawMessage
	UnknownContentParts []json.RawMessage
	UnknownContentTypes []string
	UnsupportedType     bool
}

type protocolIngressToolEvidence struct {
	Raw        json.RawMessage
	Extensions map[string]json.RawMessage
}

var responsesRequestKnownFields = map[string]struct{}{
	"model": {}, "instructions": {}, "input": {}, "tools": {}, "tool_choice": {},
	"parallel_tool_calls": {}, "stream": {}, "user": {}, "reasoning": {},
	"verbosity": {}, "previous_response_id": {}, "conversation": {}, "new_conversation": {},
}

var responsesSupportedInputTypes = map[string]struct{}{
	"": {}, "message": {}, "function_call_progress": {}, "function_call_output": {},
	"custom_tool_call_output": {}, "function_call": {}, "custom_tool_call": {},
}

func responsesInputKnownFields(typ string) map[string]struct{} {
	switch typ {
	case "function_call_progress":
		return map[string]struct{}{"type": {}, "call_id": {}, "phase": {}, "message": {}, "output": {}, "done": {}}
	case "function_call_output", "custom_tool_call_output":
		return map[string]struct{}{"type": {}, "call_id": {}, "output": {}}
	case "function_call":
		return map[string]struct{}{"type": {}, "call_id": {}, "name": {}, "arguments": {}}
	case "custom_tool_call":
		return map[string]struct{}{"type": {}, "call_id": {}, "name": {}, "input": {}}
	default:
		return map[string]struct{}{"type": {}, "role": {}, "content": {}}
	}
}

var responsesToolKnownFields = map[string]struct{}{
	"type": {}, "name": {}, "description": {}, "parameters": {}, "annotations": {},
}

func rawArray(raw json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	return items
}

func rawObjectType(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	var typ string
	_ = json.Unmarshal(object["type"], &typ)
	return typ
}

func rawObjectField(raw json.RawMessage, name string) json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return cloneRawJSON(object[name])
}

func buildResponsesInputEvidence(raw json.RawMessage) []protocolIngressItemEvidence {
	items := rawArray(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]protocolIngressItemEvidence, 0, len(items))
	for _, item := range items {
		typ := rawObjectType(item)
		_, supported := responsesSupportedInputTypes[typ]
		extensions, _ := unknownRawFields(item, responsesInputKnownFields(typ))
		contentRaw := rawObjectField(item, "content")
		unknownParts, unknownTypes := classifyUnknownContentParts(contentRaw)
		out = append(out, protocolIngressItemEvidence{
			Raw:                 cloneRawJSON(item),
			Type:                typ,
			Extensions:          extensions,
			ContentRaw:          contentRaw,
			UnknownContentParts: unknownParts,
			UnknownContentTypes: unknownTypes,
			UnsupportedType:     !supported,
		})
	}
	return out
}

func buildProtocolToolEvidence(raw json.RawMessage, known map[string]struct{}) []protocolIngressToolEvidence {
	items := rawArray(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]protocolIngressToolEvidence, 0, len(items))
	for _, item := range items {
		extensions, _ := unknownRawFields(item, known)
		out = append(out, protocolIngressToolEvidence{Raw: cloneRawJSON(item), Extensions: extensions})
	}
	return out
}

func (r *responsesRequest) UnmarshalJSON(raw []byte) error {
	type canonical responsesRequest
	var decoded canonical
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, responsesRequestKnownFields)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*r = responsesRequest(decoded)
	r.IngressRaw = cloneRawJSON(raw)
	r.IngressExtensions = extensions
	r.InputRaw = cloneRawJSON(fields["input"])
	r.InputEvidence = buildResponsesInputEvidence(r.InputRaw)
	r.ToolEvidence = buildProtocolToolEvidence(fields["tools"], responsesToolKnownFields)
	return nil
}

var anthropicMessageKnownFields = map[string]struct{}{"role": {}, "content": {}}
var anthropicToolKnownFields = map[string]struct{}{
	"name": {}, "description": {}, "input_schema": {}, "annotations": {},
}
var anthropicRequestKnownFields = map[string]struct{}{
	"model": {}, "system": {}, "messages": {}, "tools": {}, "tool_choice": {}, "stream": {}, "max_tokens": {},
}
var anthropicKnownContentTypes = map[string]struct{}{
	"text": {}, "image": {}, "tool_use": {}, "tool_result": {},
}

func classifyUnknownContentPartsWithKnown(raw json.RawMessage, known map[string]struct{}) ([]json.RawMessage, []string) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	items := rawArray(raw)
	if len(items) == 0 {
		return nil, nil
	}
	unknown := make([]json.RawMessage, 0)
	types := make([]string, 0)
	for _, item := range items {
		typ := rawObjectType(item)
		if _, ok := known[typ]; ok {
			continue
		}
		unknown = append(unknown, cloneRawJSON(item))
		if typ == "" {
			typ = "<missing>"
		}
		types = append(types, typ)
	}
	if len(unknown) == 0 {
		return nil, nil
	}
	return unknown, types
}

func (m *anthropicMessage) UnmarshalJSON(raw []byte) error {
	type canonical anthropicMessage
	var decoded canonical
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, anthropicMessageKnownFields)
	if err != nil {
		return err
	}
	*m = anthropicMessage(decoded)
	m.IngressRaw = cloneRawJSON(raw)
	m.IngressExtensions = extensions
	m.ContentRaw = rawObjectField(raw, "content")
	m.UnknownContentParts, m.UnknownContentTypes = classifyUnknownContentPartsWithKnown(m.ContentRaw, anthropicKnownContentTypes)
	return nil
}

func (t *anthropicTool) UnmarshalJSON(raw []byte) error {
	type canonical anthropicTool
	var decoded canonical
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, anthropicToolKnownFields)
	if err != nil {
		return err
	}
	*t = anthropicTool(decoded)
	t.IngressRaw = cloneRawJSON(raw)
	t.IngressExtensions = extensions
	return nil
}

func (r *anthropicRequest) UnmarshalJSON(raw []byte) error {
	type canonical anthropicRequest
	var decoded canonical
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, anthropicRequestKnownFields)
	if err != nil {
		return err
	}
	*r = anthropicRequest(decoded)
	r.IngressRaw = cloneRawJSON(raw)
	r.IngressExtensions = extensions
	r.SystemRaw = rawObjectField(raw, "system")
	r.SystemUnknownContentParts, r.SystemUnknownContentTypes = classifyUnknownContentPartsWithKnown(r.SystemRaw, anthropicKnownContentTypes)
	return nil
}

func (f *responseFormat) UnmarshalJSON(raw []byte) error {
	var decoded struct {
		Type       string         `json:"type"`
		JSONSchema map[string]any `json:"json_schema,omitempty"`
	}
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, map[string]struct{}{"type": {}, "json_schema": {}})
	if err != nil {
		return err
	}
	*f = responseFormat{
		Type:              decoded.Type,
		JSONSchema:        decoded.JSONSchema,
		IngressRaw:        cloneRawJSON(raw),
		IngressExtensions: extensions,
	}
	return nil
}

func (r *reasoningConfig) UnmarshalJSON(raw []byte) error {
	var decoded struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	}
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, map[string]struct{}{"effort": {}, "summary": {}})
	if err != nil {
		return err
	}
	*r = reasoningConfig{
		Effort:            decoded.Effort,
		Summary:           decoded.Summary,
		IngressRaw:        cloneRawJSON(raw),
		IngressExtensions: extensions,
	}
	return nil
}

func summarizeResponsesIngressEvidence(request responsesRequest) callerIngressEvidenceSummary {
	summary := callerIngressEvidenceSummary{TopLevel: len(request.IngressExtensions)}
	for name := range request.IngressExtensions {
		summary.appendName("top", name)
	}
	for _, item := range request.InputEvidence {
		if item.Type == "" || item.Type == "message" {
			summary.Message += len(item.Extensions)
			for name := range item.Extensions {
				summary.appendName("message", name)
			}
		} else {
			summary.Item += len(item.Extensions)
			for name := range item.Extensions {
				summary.appendName("item", name)
			}
		}
		if item.UnsupportedType {
			summary.Item++
			summary.appendName("item-type", item.Type)
		}
		summary.Content += len(item.UnknownContentParts)
		for _, typ := range item.UnknownContentTypes {
			summary.appendName("content", typ)
		}
	}
	for _, tool := range request.ToolEvidence {
		summary.Tool += len(tool.Extensions)
		for name := range tool.Extensions {
			summary.appendName("tool", name)
		}
	}
	if request.Reasoning != nil {
		summary.Reasoning = len(request.Reasoning.IngressExtensions)
		for name := range request.Reasoning.IngressExtensions {
			summary.appendName("reasoning", name)
		}
	}
	sort.Strings(summary.Names)
	return summary
}

func summarizeAnthropicIngressEvidence(request anthropicRequest) callerIngressEvidenceSummary {
	summary := callerIngressEvidenceSummary{TopLevel: len(request.IngressExtensions)}
	for name := range request.IngressExtensions {
		summary.appendName("top", name)
	}
	for _, message := range request.Messages {
		summary.Message += len(message.IngressExtensions)
		for name := range message.IngressExtensions {
			summary.appendName("message", name)
		}
		summary.Content += len(message.UnknownContentParts)
		for _, typ := range message.UnknownContentTypes {
			summary.appendName("content", typ)
		}
	}
	summary.Content += len(request.SystemUnknownContentParts)
	for _, typ := range request.SystemUnknownContentTypes {
		summary.appendName("content", typ)
	}
	for _, tool := range request.Tools {
		summary.Tool += len(tool.IngressExtensions)
		for name := range tool.IngressExtensions {
			summary.appendName("tool", name)
		}
	}
	sort.Strings(summary.Names)
	return summary
}

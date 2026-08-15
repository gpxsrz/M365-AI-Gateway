package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var openAIRequestKnownFields = map[string]struct{}{
	"model": {}, "response_format": {}, "messages": {}, "stream": {}, "user": {},
	"stream_options":  {},
	"conversation_id": {}, "session_id": {}, "session_key": {}, "attachments": {},
	"tools": {}, "functions": {}, "tool_choice": {}, "parallel_tool_calls": {},
	"function_call": {}, "reasoning": {}, "reasoning_effort": {}, "verbosity": {},
	"temperature": {}, "top_p": {}, "max_tokens": {}, "max_completion_tokens": {},
	"stop": {}, "seed": {}, "frequency_penalty": {}, "presence_penalty": {},
}

var openAIMessageKnownFields = map[string]struct{}{
	"role": {}, "content": {}, "name": {}, "tool_call_id": {}, "tool_calls": {},
}

var openAIKnownContentTypes = map[string]struct{}{
	"text": {}, "input_text": {}, "output_text": {},
	"image_url": {}, "input_image": {}, "image": {},
	"input_file": {}, "file": {},
	"input_audio": {}, "audio": {},
}

func cloneRawJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func decodeUseNumberValue(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func unknownRawFields(raw []byte, known map[string]struct{}) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage)
	for name, value := range fields {
		if _, ok := known[name]; ok {
			continue
		}
		out[name] = cloneRawJSON(value)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (m *oaiMsg) UnmarshalJSON(raw []byte) error {
	type canonical oaiMsg
	var decoded canonical
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, openAIMessageKnownFields)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*m = oaiMsg(decoded)
	m.Content = canonicalizeOpenAIContent(m.Content)
	m.ToolCalls = canonicalIngressToolCalls(m.ToolCalls)
	m.IngressRaw = cloneRawJSON(raw)
	m.IngressExtensions = extensions
	m.ContentRaw = cloneRawJSON(fields["content"])
	m.UnknownContentParts, m.UnknownContentTypes = classifyUnknownContentParts(m.ContentRaw)
	return nil
}

func canonicalizeOpenAIContent(content any) any {
	parts, ok := content.([]any)
	if !ok {
		return content
	}
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		object, ok := part.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := object["type"].(string)
		if typ == "" {
			if _, ok := object["text"].(string); ok {
				out = append(out, part)
			}
			continue
		}
		if _, supported := openAIKnownContentTypes[typ]; !supported {
			continue
		}
		out = append(out, part)
	}
	return out
}

func canonicalIngressToolCalls(calls []map[string]any) []map[string]any {
	if calls == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		canonical := map[string]any{}
		if id, ok := call["id"]; ok {
			canonical["id"] = id
		}
		if typ, ok := call["type"]; ok {
			canonical["type"] = typ
		}
		if function, ok := call["function"].(map[string]any); ok {
			fn := map[string]any{}
			if name, exists := function["name"]; exists {
				fn["name"] = name
			}
			if arguments, exists := function["arguments"]; exists {
				fn["arguments"] = arguments
			}
			canonical["function"] = fn
		}
		out = append(out, canonical)
	}
	return out
}

func classifyUnknownContentParts(raw json.RawMessage) ([]json.RawMessage, []string) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, nil
	}
	unknown := make([]json.RawMessage, 0)
	types := make([]string, 0)
	for _, part := range parts {
		var object map[string]json.RawMessage
		if json.Unmarshal(part, &object) != nil {
			unknown = append(unknown, cloneRawJSON(part))
			types = append(types, "<non_object>")
			continue
		}
		var typ string
		if rawType := object["type"]; len(rawType) > 0 {
			_ = json.Unmarshal(rawType, &typ)
		}
		if typ == "" {
			var text string
			if rawText := object["text"]; len(rawText) > 0 && json.Unmarshal(rawText, &text) == nil {
				continue
			}
		}
		if _, known := openAIKnownContentTypes[typ]; known {
			continue
		}
		unknown = append(unknown, cloneRawJSON(part))
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

func (r *oaiReq) UnmarshalJSON(raw []byte) error {
	type canonical oaiReq
	var decoded canonical
	if err := decodeUseNumberValue(raw, &decoded); err != nil {
		return err
	}
	extensions, err := unknownRawFields(raw, openAIRequestKnownFields)
	if err != nil {
		return err
	}
	*r = oaiReq(decoded)
	r.IngressRaw = cloneRawJSON(raw)
	r.IngressExtensions = extensions
	return nil
}

type callerIngressEvidenceSummary struct {
	TopLevel  int
	Message   int
	Item      int
	Content   int
	Tool      int
	Format    int
	Reasoning int
	Names     []string
}

func safeIngressEvidenceName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func summarizeCallerIngressEvidence(body oaiReq) callerIngressEvidenceSummary {
	summary := callerIngressEvidenceSummary{TopLevel: len(body.IngressExtensions)}
	for name := range body.IngressExtensions {
		summary.appendName("top", name)
	}
	for _, message := range body.Messages {
		summary.Message += len(message.IngressExtensions)
		summary.Content += len(message.UnknownContentParts)
		for name := range message.IngressExtensions {
			summary.appendName("message", name)
		}
		for _, typ := range message.UnknownContentTypes {
			summary.appendName("content", typ)
		}
	}
	for _, tool := range body.Tools {
		summary.Tool += len(tool.IngressExtensions) + len(tool.FunctionExtensions)
		for name := range tool.IngressExtensions {
			summary.appendName("tool", name)
		}
		for name := range tool.FunctionExtensions {
			summary.appendName("tool-function", name)
		}
	}
	if body.ResponseFormat != nil {
		summary.Format = len(body.ResponseFormat.IngressExtensions)
		for name := range body.ResponseFormat.IngressExtensions {
			summary.appendName("format", name)
		}
	}
	if body.Reasoning != nil {
		summary.Reasoning = len(body.Reasoning.IngressExtensions)
		for name := range body.Reasoning.IngressExtensions {
			summary.appendName("reasoning", name)
		}
	}
	sort.Strings(summary.Names)
	return summary
}

func (summary *callerIngressEvidenceSummary) appendName(kind, name string) {
	if len(summary.Names) >= 32 || !safeIngressEvidenceName(name) {
		return
	}
	summary.Names = append(summary.Names, kind+":"+name)
}

func (summary callerIngressEvidenceSummary) total() int {
	return summary.TopLevel + summary.Message + summary.Item + summary.Content + summary.Tool + summary.Format + summary.Reasoning
}

func setIngressEvidenceSummaryHeaders(w http.ResponseWriter, summary callerIngressEvidenceSummary) callerIngressEvidenceSummary {
	if summary.total() == 0 {
		return summary
	}
	const countsHeader = "X-M365-Preserved-Extension-Counts"
	const namesHeader = "X-M365-Preserved-Extension-Names"
	w.Header().Set(countsHeader,
		"top="+strconv.Itoa(summary.TopLevel)+
			",message="+strconv.Itoa(summary.Message)+
			",item="+strconv.Itoa(summary.Item)+
			",content="+strconv.Itoa(summary.Content)+
			",tool="+strconv.Itoa(summary.Tool)+
			",format="+strconv.Itoa(summary.Format)+
			",reasoning="+strconv.Itoa(summary.Reasoning))
	exposeCompatibilityHeader(w, countsHeader)
	if len(summary.Names) > 0 {
		w.Header().Set(namesHeader, strings.Join(summary.Names, ","))
		exposeCompatibilityHeader(w, namesHeader)
	}
	return summary
}

func setCallerIngressEvidenceHeaders(w http.ResponseWriter, body oaiReq) callerIngressEvidenceSummary {
	return setIngressEvidenceSummaryHeaders(w, summarizeCallerIngressEvidence(body))
}

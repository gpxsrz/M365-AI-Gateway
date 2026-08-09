package chathub

import "encoding/json"

// classifyUpdateMessages converts a ChatHub messages array into protocol-neutral
// events. It deliberately does not infer tools from ordinary prose.
func classifyUpdateMessages(messages []any) []StreamEvent {
	var out []StreamEvent
	for _, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text, _ := m["text"].(string)
		mt, _ := m["messageType"].(string)
		ct, _ := m["contentType"].(string)
		origin, _ := m["contentOrigin"].(string)
		addToChainOfThought, _ := m["addToChainOfThought"].(bool)
		// A mixed provider message may carry safe progress/text beside a
		// protected Code Interpreter artifact child. This projection exposes
		// only the selected text/tool fields, so reject the whole message only
		// when the message itself is the artifact or its visible text is
		// protected. Field-level traversal handles protected siblings elsewhere.
		if generatedCodeInterpreterMessage(m) || ContainsProtectedArtifactReference(text) {
			continue
		}
		kind := "text"
		if reasoningSummaryMessage(mt, origin, addToChainOfThought, text) {
			kind = "reasoning"
		} else if mt == "Progress" || ct == "SearchResults" || ct == "Code" || ct == "ToolCall" {
			kind = "progress"
		}
		name, args := extractToolFields(m)
		if name != "" {
			kind = "tool"
		}
		if text == "" && kind == "text" {
			continue
		}
		out = append(out, StreamEvent{Kind: kind, Text: text, MessageType: mt, ContentType: ct, ToolName: name, Arguments: args})
	}
	return out
}

func extractToolFields(m map[string]any) (string, json.RawMessage) {
	var name, nameKey string
	for _, k := range []string{"name", "toolName", "pluginName", "functionName"} {
		if v, ok := m[k].(string); ok && v != "" {
			name = v
			nameKey = k
			break
		}
	}
	if name == "" {
		return "", nil
	}
	for _, k := range []string{"arguments", "args", "parameters", "input", "functionArguments"} {
		if v, ok := m[k]; ok {
			b, err := json.Marshal(v)
			if err == nil && len(b) > 0 {
				return name, b
			}
		}
	}
	if argumentlessToolShape(m, nameKey) {
		return name, nil
	}
	return "", nil
}

func argumentlessToolShape(m map[string]any, nameKey string) bool {
	if nameKey != "name" {
		return true
	}
	messageType, _ := m["messageType"].(string)
	contentType, _ := m["contentType"].(string)
	target, _ := m["target"].(string)
	return messageType == "Progress" || contentType == "ToolCall" || target == "plugin"
}

func eventRaw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// extractToolEvents walks the complete SignalR update argument. ChatHub often
// places native plugin calls outside messages[], so looking only at messages
// loses the call after the assistant's preamble.
func extractToolEvents(v any, seen map[string]bool) []StreamEvent {
	var out []StreamEvent
	var walk func(any)
	walk = func(x any) {
		switch z := x.(type) {
		case []any:
			for _, item := range z {
				walk(item)
			}
		case map[string]any:
			if generatedCodeInterpreterMessage(z) {
				return
			}
			name, args := extractToolFields(z)
			if name != "" {
				if len(args) == 0 || !ContainsProtectedArtifactJSON(args) {
					key := name + "|" + string(args)
					if !seen[key] {
						seen[key] = true
						messageType, _ := z["messageType"].(string)
						contentType, _ := z["contentType"].(string)
						raw := eventRaw(z)
						if artifactBearingMap(z) {
							raw = nil
						}
						out = append(out, StreamEvent{Kind: "tool", MessageType: messageType, ContentType: contentType, ToolName: name, Arguments: args, Raw: raw})
					}
				}
				return
			}
			for key, child := range z {
				if IsProtectedArtifactField(key, child) {
					continue
				}
				walk(child)
			}
		}
	}
	walk(v)
	return out
}

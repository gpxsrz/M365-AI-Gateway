package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"m365-native/internal/chathub"
)

var errNativeMutationBlocked = errors.New("native mutation blocked at sidecar emission")

type blockedNativeToolCall struct {
	Name   string
	Reason string
}

type nativeToolCallScan struct {
	Calls            []detectedToolCall
	ReadOnlyObserved int
	Blocked          []blockedNativeToolCall
}

// nativeToolCalls preserves the compatibility interface used by result
// validation. Enforcement-aware callers should use scanNativeToolCalls.
func nativeToolCalls(events []json.RawMessage, tools []chathub.Tool) []detectedToolCall {
	return scanNativeToolCalls(events, tools).Calls
}

// scanNativeToolCalls accepts caller-declared client tool calls, records
// structurally identified SearchResults as read-only observations, and blocks
// every other undeclared tool-shaped upstream event before it can be emitted as
// a caller-executable tool call.
func scanNativeToolCalls(events []json.RawMessage, tools []chathub.Tool) nativeToolCallScan {
	declared := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if name := nativePolicyToolName(tool); name != "" {
			declared[name] = struct{}{}
		}
	}

	scan := nativeToolCallScan{}
	seenCalls := map[string]struct{}{}
	seenBlocked := map[string]struct{}{}
	for _, raw := range events {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			walkNativeToolEvent(value, declared, seenCalls, seenBlocked, &scan)
		}
	}
	return scan
}

func walkNativeToolEvent(value any, declared, seenCalls, seenBlocked map[string]struct{}, scan *nativeToolCallScan) {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			walkNativeToolEvent(child, declared, seenCalls, seenBlocked, scan)
		}
	case map[string]any:
		if chathub.IsGeneratedCodeInterpreterMessage(node) {
			return
		}
		name, arguments := nativeToolEventFields(node, declared)
		if name != "" {
			if len(arguments) > 0 && chathub.ContainsProtectedArtifactJSON(arguments) {
				return
			}
			if len(arguments) == 0 {
				key := strings.ToLower(name) + "|<missing_arguments>"
				if _, duplicate := seenBlocked[key]; !duplicate {
					seenBlocked[key] = struct{}{}
					scan.Blocked = append(scan.Blocked, blockedNativeToolCall{Name: name, Reason: "missing_arguments"})
				}
				return
			}
			key := name + "|" + string(arguments)
			if _, exists := declared[strings.ToLower(name)]; exists {
				if _, duplicate := seenCalls[key]; !duplicate {
					seenCalls[key] = struct{}{}
					hash := sha256.Sum256([]byte(key))
					scan.Calls = append(scan.Calls, detectedToolCall{ID: "call_" + hex.EncodeToString(hash[:8]), Name: name, Arguments: arguments})
				}
				return
			}
			if nativeReadOnlyObservation(node, name) {
				scan.ReadOnlyObserved++
				return
			}
			if _, duplicate := seenBlocked[key]; !duplicate {
				seenBlocked[key] = struct{}{}
				scan.Blocked = append(scan.Blocked, blockedNativeToolCall{Name: name, Reason: "undeclared_native_tool"})
			}
			return
		}
		for key, child := range node {
			if chathub.IsProtectedArtifactField(key, child) {
				continue
			}
			walkNativeToolEvent(child, declared, seenCalls, seenBlocked, scan)
		}
	}
}

func nativeToolEventFields(node map[string]any, declared map[string]struct{}) (string, json.RawMessage) {
	name, nameKey := "", ""
	for _, key := range []string{"name", "toolName", "pluginName", "functionName"} {
		if value, ok := node[key].(string); ok && strings.TrimSpace(value) != "" {
			name = strings.TrimSpace(value)
			nameKey = key
			break
		}
	}
	if name == "" {
		if value, ok := node["id"].(string); ok {
			if _, declaredID := declared[strings.ToLower(strings.TrimSpace(value))]; declaredID {
				name = strings.TrimSpace(value)
				nameKey = "id"
			}
		}
	}
	if name == "" {
		return "", nil
	}
	for _, key := range []string{"arguments", "args", "parameters", "input", "functionArguments"} {
		if value, ok := node[key]; ok {
			raw, err := json.Marshal(value)
			if err == nil && len(raw) > 0 {
				return name, raw
			}
		}
	}
	if nativeArgumentlessToolShape(node, nameKey) {
		return name, nil
	}
	return "", nil
}

func nativeArgumentlessToolShape(node map[string]any, nameKey string) bool {
	if nameKey != "name" {
		return true
	}
	messageType, _ := node["messageType"].(string)
	contentType, _ := node["contentType"].(string)
	target, _ := node["target"].(string)
	return messageType == "Progress" || contentType == "ToolCall" || target == "plugin"
}

func nativeReadOnlyObservation(node map[string]any, name string) bool {
	contentType, _ := node["contentType"].(string)
	return nativeReadOnlyToolIdentity(name, contentType)
}

func nativeReadOnlyToolIdentity(name, contentType string) bool {
	if contentType != "SearchResults" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "web_search", "bing_search":
		return true
	default:
		return false
	}
}

func classifyNativeStreamToolEvent(event chathub.StreamEvent, tools []chathub.Tool) (emitClientCall bool, readOnlyObservation bool, err error) {
	if event.Kind != "tool" || strings.TrimSpace(event.ToolName) == "" {
		return false, false, nil
	}
	if len(event.Arguments) == 0 {
		return false, false, fmt.Errorf("%w: %s (missing arguments)", errNativeMutationBlocked, event.ToolName)
	}
	if chathub.ContainsProtectedArtifactJSON(event.Arguments) {
		return false, false, fmt.Errorf("%w: protected artifact metadata", errNativeMutationBlocked)
	}
	name := strings.ToLower(strings.TrimSpace(event.ToolName))
	for _, tool := range tools {
		if nativePolicyToolName(tool) == name {
			return true, false, nil
		}
	}
	if nativeReadOnlyToolIdentity(event.ToolName, event.ContentType) {
		return false, true, nil
	}
	return false, false, fmt.Errorf("%w: %s", errNativeMutationBlocked, event.ToolName)
}

func blockedNativeToolError(scan nativeToolCallScan) error {
	if len(scan.Blocked) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errNativeMutationBlocked, scan.Blocked[0].Name)
}

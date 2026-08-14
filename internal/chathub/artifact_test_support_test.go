package chathub

import (
	"encoding/json"
)

func directArtifactBearingMap(value map[string]any) bool {
	if generatedCodeInterpreterMessage(value) {
		return true
	}
	for key, child := range value {
		if IsProtectedArtifactField(key, child) {
			return true
		}
	}
	return false
}

func ContainsDirectProtectedArtifactJSON(raw json.RawMessage) bool {
	var value map[string]any
	return len(raw) == 0 || json.Unmarshal(raw, &value) != nil || directArtifactBearingMap(value)
}

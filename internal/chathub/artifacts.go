package chathub

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const (
	maxGeneratedArtifacts              = 32
	maxGeneratedArtifactReferenceBytes = 512
	maxGeneratedArtifactURLBytes       = 16 << 10
	maxGeneratedArtifactFilenameBytes  = 1024
	maxGeneratedArtifactTextJSONBytes  = 1 << 20
	maxGeneratedArtifactScanNodes      = 64 << 10
	maxGeneratedArtifactScanDepth      = 32
)

var errInvalidGeneratedArtifactMetadata = errors.New("invalid generated artifact metadata")

// GeneratedArtifact is the upstream identity needed to retrieve one Code
// Interpreter output. File outputs use CodeResultFileURL while generated image
// outputs use CodeResultImageURL. Both are protected transport metadata;
// callers must receive a Sidecar-owned download URL instead.
type GeneratedArtifact struct {
	ReferenceID        string
	CodeResultFileURL  string
	CodeResultImageURL string
	Filename           string
}

// GeneratedArtifacts extracts only structured Code Interpreter output-file
// metadata. SignalR prose is never scanned for file-looking text. rawResult is
// considered only when the complete value is valid JSON containing an
// outputFiles array.
func GeneratedArtifacts(raw []json.RawMessage, rawResult string) ([]GeneratedArtifact, error) {
	collector := generatedArtifactCollector{byReference: map[string]int{}}
	for _, frame := range raw {
		var value any
		if json.Unmarshal(frame, &value) != nil {
			continue
		}
		collector.nodes = 0
		if err := collector.walkTypedMessages(value, 0); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(rawResult) != "" {
		var value any
		if json.Unmarshal([]byte(rawResult), &value) == nil {
			switch value.(type) {
			case map[string]any, []any:
				collector.nodes = 0
				if err := collector.collectOutputFiles(value, 0); err != nil {
					return nil, err
				}
			}
		}
	}
	return collector.artifacts, nil
}

type generatedArtifactCollector struct {
	artifacts   []GeneratedArtifact
	byReference map[string]int
	nodes       int
}

func (c *generatedArtifactCollector) visit(depth int) error {
	c.nodes++
	if depth > maxGeneratedArtifactScanDepth || c.nodes > maxGeneratedArtifactScanNodes {
		return errInvalidGeneratedArtifactMetadata
	}
	return nil
}

func (c *generatedArtifactCollector) walkTypedMessages(value any, depth int) error {
	if err := c.visit(depth); err != nil {
		return err
	}
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if err := c.walkTypedMessages(child, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		if generatedCodeInterpreterMessage(node) {
			if err := c.collectOutputFiles(node, depth+1); err != nil {
				return err
			}
			if text, ok := node["text"].(string); ok {
				if len(text) > maxGeneratedArtifactTextJSONBytes {
					if ContainsProtectedArtifactReference(text) {
						return errInvalidGeneratedArtifactMetadata
					}
				} else {
					var structured any
					if json.Unmarshal([]byte(text), &structured) == nil {
						switch structured.(type) {
						case map[string]any, []any:
							if err := c.collectOutputFiles(structured, depth+1); err != nil {
								return err
							}
						}
					}
				}
			}
			return nil
		}
		for _, key := range sortedArtifactKeys(node) {
			child := node[key]
			if err := c.walkTypedMessages(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *generatedArtifactCollector) collectOutputFiles(value any, depth int) error {
	if err := c.visit(depth); err != nil {
		return err
	}
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if err := c.collectOutputFiles(child, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, key := range sortedArtifactKeys(node) {
			child := node[key]
			if key == "outputFiles" {
				files, ok := child.([]any)
				if !ok {
					return errInvalidGeneratedArtifactMetadata
				}
				for _, rawFile := range files {
					file, ok := rawFile.(map[string]any)
					if !ok {
						return errInvalidGeneratedArtifactMetadata
					}
					if err := c.add(file); err != nil {
						return err
					}
				}
				continue
			}
			if err := c.collectOutputFiles(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *generatedArtifactCollector) add(value map[string]any) error {
	referenceID, referenceOK := value["reference_id"].(string)
	fileURL, fileURLOK := value["codeResultFileUrl"].(string)
	imageURL, imageURLOK := value["codeResultImageUrl"].(string)
	if _, present := value["codeResultFileUrl"]; present && !fileURLOK {
		return errInvalidGeneratedArtifactMetadata
	}
	if _, present := value["codeResultImageUrl"]; present && !imageURLOK {
		return errInvalidGeneratedArtifactMetadata
	}
	filename, filenameOK := value["filename"].(string)
	if _, present := value["filename"]; present && !filenameOK {
		return errInvalidGeneratedArtifactMetadata
	}
	if filename == "" {
		if liveFilename, ok := value["fileName"].(string); ok {
			filename = liveFilename
		} else if _, present := value["fileName"]; present {
			return errInvalidGeneratedArtifactMetadata
		}
	}
	fileURL = strings.TrimSpace(fileURL)
	imageURL = strings.TrimSpace(imageURL)
	if !referenceOK || strings.TrimSpace(referenceID) == "" || (fileURL == "") == (imageURL == "") ||
		len(referenceID) > maxGeneratedArtifactReferenceBytes || len(fileURL) > maxGeneratedArtifactURLBytes || len(imageURL) > maxGeneratedArtifactURLBytes || len(filename) > maxGeneratedArtifactFilenameBytes {
		return errInvalidGeneratedArtifactMetadata
	}
	artifact := GeneratedArtifact{ReferenceID: referenceID, CodeResultFileURL: fileURL, CodeResultImageURL: imageURL, Filename: filename}
	if index, exists := c.byReference[referenceID]; exists {
		existing := c.artifacts[index]
		if existing.CodeResultFileURL != artifact.CodeResultFileURL || existing.CodeResultImageURL != artifact.CodeResultImageURL || (existing.Filename != "" && artifact.Filename != "" && existing.Filename != artifact.Filename) {
			return errInvalidGeneratedArtifactMetadata
		}
		if existing.Filename == "" && artifact.Filename != "" {
			c.artifacts[index].Filename = artifact.Filename
		}
		return nil
	}
	if len(c.artifacts) >= maxGeneratedArtifacts {
		return errInvalidGeneratedArtifactMetadata
	}
	c.byReference[referenceID] = len(c.artifacts)
	c.artifacts = append(c.artifacts, artifact)
	return nil
}

func generatedCodeInterpreterMessage(value map[string]any) bool {
	messageType, _ := value["messageType"].(string)
	contentOrigin, _ := value["contentOrigin"].(string)
	return strings.EqualFold(messageType, "GeneratedCode") && strings.EqualFold(contentOrigin, "CodeInterpreter")
}

// IsGeneratedCodeInterpreterMessage identifies a provider message whose whole
// object is Code Interpreter artifact metadata rather than a mixed container.
func IsGeneratedCodeInterpreterMessage(value map[string]any) bool {
	return generatedCodeInterpreterMessage(value)
}

func sortedArtifactKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ContainsProtectedArtifactReference is a conservative caller-boundary guard.
// It detects upstream artifact keys and URL families; extraction and URL
// validation remain structural concerns handled separately.
func ContainsProtectedArtifactReference(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "coderesultfileurl") ||
		strings.Contains(lower, "coderesultimageurl") ||
		strings.Contains(lower, "asyncgw.teams.microsoft.com") ||
		strings.Contains(lower, "blob:")
}

func artifactBearingMessage(value map[string]any, text string, queries []string) bool {
	if generatedCodeInterpreterMessage(value) || artifactBearingMap(value) || ContainsProtectedArtifactReference(text) {
		return true
	}
	for _, query := range queries {
		if ContainsProtectedArtifactReference(query) {
			return true
		}
	}
	return false
}

func artifactBearingMap(value map[string]any) bool {
	nodes := 0
	return containsProtectedArtifactValue(value, 0, &nodes)
}

// IsProtectedArtifactField identifies one protected artifact child. Traversal
// code can skip this child while still preserving unrelated safe siblings.
func IsProtectedArtifactField(key string, child any) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "outputfiles", "coderesultfileurl", "coderesultimageurl":
		return true
	}
	text, ok := child.(string)
	return ok && ContainsProtectedArtifactReference(text)
}

// ContainsProtectedArtifactJSON applies the same bounded structural guard to
// caller-visible tool arguments. Invalid or overly deep input fails closed.
func ContainsProtectedArtifactJSON(raw json.RawMessage) bool {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return true
	}
	nodes := 0
	return containsProtectedArtifactValue(value, 0, &nodes)
}

func containsProtectedArtifactValue(value any, depth int, nodes *int) bool {
	(*nodes)++
	if depth > maxGeneratedArtifactScanDepth || *nodes > maxGeneratedArtifactScanNodes {
		return true
	}
	switch node := value.(type) {
	case string:
		return ContainsProtectedArtifactReference(node)
	case []any:
		for _, child := range node {
			if containsProtectedArtifactValue(child, depth+1, nodes) {
				return true
			}
		}
	case map[string]any:
		if generatedCodeInterpreterMessage(node) {
			return true
		}
		for key, child := range node {
			if IsProtectedArtifactField(key, child) || containsProtectedArtifactValue(child, depth+1, nodes) {
				return true
			}
		}
	}
	return false
}

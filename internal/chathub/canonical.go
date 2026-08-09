package chathub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const maxCanonicalAttributions = 128

type Artifact struct {
	Kind               string          `json:"kind"`
	ReferenceID        string          `json:"referenceId,omitempty"`
	Filename           string          `json:"filename,omitempty"`
	MIMEType           string          `json:"mimeType,omitempty"`
	CodeResultFileURL  string          `json:"-"`
	CodeResultImageURL string          `json:"-"`
	DownloadURL        string          `json:"-"`
	WebURL             string          `json:"-"`
	PollURL            string          `json:"-"`
	FileToken          string          `json:"-"`
	PublicURL          string          `json:"url,omitempty"`
	Raw                json.RawMessage `json:"-"`
}

func (a Artifact) upstreamDownloadURL() string {
	if strings.TrimSpace(a.CodeResultFileURL) != "" {
		return a.CodeResultFileURL
	}
	if strings.TrimSpace(a.CodeResultImageURL) != "" {
		return a.CodeResultImageURL
	}
	return a.DownloadURL
}

type Attribution struct {
	Kind              string          `json:"kind"`
	ID                string          `json:"id,omitempty"`
	TargetLink        string          `json:"targetLink,omitempty"`
	IsCitedInResponse bool            `json:"isCitedInResponse,omitempty"`
	DisplayData       map[string]any  `json:"displayData,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

func CanonicalizeResult(result *Result) error {
	if result == nil {
		return nil
	}
	result.Normalized = NormalizeEvents(result.Events)
	result.Terminal = terminalStateFromEvents(result.Events, result.Terminal)
	artifacts, err := canonicalArtifacts(result.Events, result.RawResult, result.Images)
	if err != nil {
		return err
	}
	result.Artifacts = artifacts
	result.Attributions = canonicalAttributions(result.Events, result.RawResult)
	result.UnknownEvents = result.UnknownEvents[:0]
	for _, event := range result.Normalized {
		if event.Kind == "unknown" || !knownSignalRType(event.Type) {
			result.UnknownEvents = append(result.UnknownEvents, event)
		}
	}
	return nil
}

func knownSignalRType(messageType int) bool {
	switch messageType {
	case 1, 2, 3, 6, 7:
		return true
	default:
		return false
	}
}

func canonicalArtifacts(raw []json.RawMessage, rawResult string, images []string) ([]Artifact, error) {
	collector := canonicalArtifactCollector{byIdentity: map[string]int{}, byReference: map[string]int{}}
	generated, err := GeneratedArtifacts(raw, rawResult)
	if err != nil {
		return nil, err
	}
	for _, artifact := range generated {
		kind := "file"
		if artifact.CodeResultImageURL != "" {
			kind = "image"
		}
		if err := collector.add(Artifact{
			Kind:               kind,
			ReferenceID:        artifact.ReferenceID,
			Filename:           artifact.Filename,
			CodeResultFileURL:  artifact.CodeResultFileURL,
			CodeResultImageURL: artifact.CodeResultImageURL,
		}, nil); err != nil {
			return nil, err
		}
	}
	for _, frame := range raw {
		var value any
		if json.Unmarshal(frame, &value) != nil {
			continue
		}
		collector.nodes = 0
		if err := collector.walk(value, 0, ""); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(rawResult) != "" {
		var value any
		if json.Unmarshal([]byte(rawResult), &value) == nil {
			collector.nodes = 0
			if err := collector.walk(value, 0, "rawResult"); err != nil {
				return nil, err
			}
		}
	}
	for _, imageURL := range images {
		imageURL = strings.TrimSpace(imageURL)
		if !IsImageURL(imageURL) {
			continue
		}
		if err := collector.add(Artifact{Kind: "image", PublicURL: imageURL}, nil); err != nil {
			return nil, err
		}
	}
	return collector.artifacts, nil
}

type canonicalArtifactCollector struct {
	artifacts   []Artifact
	byIdentity  map[string]int
	byReference map[string]int
	nodes       int
}

func (c *canonicalArtifactCollector) visit(depth int) error {
	c.nodes++
	if depth > maxGeneratedArtifactScanDepth || c.nodes > maxGeneratedArtifactScanNodes {
		return errInvalidGeneratedArtifactMetadata
	}
	return nil
}

func (c *canonicalArtifactCollector) walk(value any, depth int, parentKey string) error {
	if err := c.visit(depth); err != nil {
		return err
	}
	switch node := value.(type) {
	case string:
		text := strings.TrimSpace(node)
		if len(text) == 0 || len(text) > maxGeneratedArtifactTextJSONBytes || (text[0] != '{' && text[0] != '[') {
			return nil
		}
		var nested any
		if json.Unmarshal([]byte(text), &nested) != nil {
			return nil
		}
		return c.walk(nested, depth+1, parentKey)
	case []any:
		for _, child := range node {
			if err := c.walk(child, depth+1, parentKey); err != nil {
				return err
			}
		}
	case map[string]any:
		if artifact, ok := artifactFromMap(node, parentKey); ok {
			if err := c.add(artifact, node); err != nil {
				return err
			}
		}
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := c.walk(node[key], depth+1, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func artifactFromMap(value map[string]any, parentKey string) (Artifact, bool) {
	stringField := func(names ...string) string {
		for _, name := range names {
			if raw, ok := value[name]; ok {
				if text, ok := raw.(string); ok {
					return strings.TrimSpace(text)
				}
			}
		}
		return ""
	}
	artifact := Artifact{
		ReferenceID:        stringField("reference_id", "referenceId"),
		Filename:           stringField("filename", "fileName", "name"),
		MIMEType:           stringField("mimeType", "mime_type", "contentType"),
		CodeResultFileURL:  stringField("codeResultFileUrl"),
		CodeResultImageURL: stringField("codeResultImageUrl"),
		DownloadURL:        stringField("downloadUrl", "downloadURL"),
		WebURL:             stringField("webUrl", "webURL"),
		PollURL:            stringField("pollUrl", "pollURL"),
		FileToken:          stringField("fileToken", "file_token"),
	}
	transportBearing := artifact.CodeResultFileURL != "" || artifact.CodeResultImageURL != "" || artifact.DownloadURL != "" || artifact.WebURL != "" || artifact.PollURL != "" || artifact.FileToken != ""
	if !transportBearing && !strings.EqualFold(parentKey, "outputFiles") {
		return Artifact{}, false
	}
	if !transportBearing && artifact.ReferenceID == "" && artifact.Filename == "" {
		return Artifact{}, false
	}
	artifact.Kind = "file"
	if artifact.CodeResultImageURL != "" || strings.HasPrefix(strings.ToLower(artifact.MIMEType), "image/") || IsImageURL(artifact.DownloadURL) || IsImageURL(artifact.WebURL) {
		artifact.Kind = "image"
	}
	raw, err := json.Marshal(value)
	if err == nil {
		artifact.Raw = raw
	}
	return artifact, true
}

func (c *canonicalArtifactCollector) add(artifact Artifact, raw map[string]any) error {
	if len(c.artifacts) >= maxGeneratedArtifacts {
		return errInvalidGeneratedArtifactMetadata
	}
	if artifact.ReferenceID != "" {
		if index, exists := c.byReference[artifact.ReferenceID]; exists {
			existing := c.artifacts[index]
			if artifactTransportConflict(existing, artifact) {
				return errInvalidGeneratedArtifactMetadata
			}
			c.artifacts[index] = mergeArtifact(existing, artifact)
			return nil
		}
	}
	identity := artifactIdentity(artifact)
	if index, exists := c.byIdentity[identity]; exists {
		c.artifacts[index] = mergeArtifact(c.artifacts[index], artifact)
		return nil
	}
	if artifact.ReferenceID != "" {
		c.byReference[artifact.ReferenceID] = len(c.artifacts)
	}
	c.byIdentity[identity] = len(c.artifacts)
	c.artifacts = append(c.artifacts, artifact)
	_ = raw
	return nil
}

func artifactTransportConflict(a, b Artifact) bool {
	for _, pair := range [][2]string{{a.CodeResultFileURL, b.CodeResultFileURL}, {a.CodeResultImageURL, b.CodeResultImageURL}, {a.DownloadURL, b.DownloadURL}, {a.WebURL, b.WebURL}, {a.PollURL, b.PollURL}, {a.FileToken, b.FileToken}} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return true
		}
	}
	return false
}

func mergeArtifact(dst, src Artifact) Artifact {
	if dst.Kind == "" {
		dst.Kind = src.Kind
	}
	if dst.ReferenceID == "" {
		dst.ReferenceID = src.ReferenceID
	}
	if dst.Filename == "" {
		dst.Filename = src.Filename
	}
	if dst.MIMEType == "" {
		dst.MIMEType = src.MIMEType
	}
	if dst.CodeResultFileURL == "" {
		dst.CodeResultFileURL = src.CodeResultFileURL
	}
	if dst.CodeResultImageURL == "" {
		dst.CodeResultImageURL = src.CodeResultImageURL
	}
	if dst.DownloadURL == "" {
		dst.DownloadURL = src.DownloadURL
	}
	if dst.WebURL == "" {
		dst.WebURL = src.WebURL
	}
	if dst.PollURL == "" {
		dst.PollURL = src.PollURL
	}
	if dst.FileToken == "" {
		dst.FileToken = src.FileToken
	}
	if dst.PublicURL == "" {
		dst.PublicURL = src.PublicURL
	}
	if len(dst.Raw) == 0 {
		dst.Raw = append(json.RawMessage(nil), src.Raw...)
	}
	return dst
}

func artifactIdentity(artifact Artifact) string {
	identity := strings.Join([]string{artifact.Kind, artifact.ReferenceID, artifact.CodeResultFileURL, artifact.CodeResultImageURL, artifact.DownloadURL, artifact.WebURL, artifact.PollURL, artifact.FileToken, artifact.PublicURL, artifact.Filename}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func canonicalAttributions(raw []json.RawMessage, rawResult string) []Attribution {
	collector := attributionCollector{seen: map[string]struct{}{}}
	for _, frame := range raw {
		var value any
		if json.Unmarshal(frame, &value) == nil {
			collector.nodes = 0
			collector.walk(value, 0, "")
		}
	}
	if strings.TrimSpace(rawResult) != "" {
		var value any
		if json.Unmarshal([]byte(rawResult), &value) == nil {
			collector.nodes = 0
			collector.walk(value, 0, "")
		}
	}
	return collector.items
}

type attributionCollector struct {
	items []Attribution
	seen  map[string]struct{}
	nodes int
}

func (c *attributionCollector) walk(value any, depth int, parentKey string) {
	if len(c.items) >= maxCanonicalAttributions || depth > maxGeneratedArtifactScanDepth || c.nodes > maxGeneratedArtifactScanNodes {
		return
	}
	c.nodes++
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			c.walk(child, depth+1, parentKey)
		}
	case map[string]any:
		if isAttributionContainer(parentKey) {
			c.add(parentKey, node)
		}
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := node[key]
			if isAttributionContainer(key) {
				switch nested := child.(type) {
				case map[string]any:
					if attributionLeaf(nested) {
						c.add(key, nested)
					} else {
						for id, rawLeaf := range nested {
							if leaf, ok := rawLeaf.(map[string]any); ok {
								copyLeaf := make(map[string]any, len(leaf)+1)
								for k, v := range leaf {
									copyLeaf[k] = v
								}
								if _, exists := copyLeaf["id"]; !exists {
									copyLeaf["id"] = id
								}
								c.add(key, copyLeaf)
							}
						}
					}
				case []any:
					for _, rawLeaf := range nested {
						if leaf, ok := rawLeaf.(map[string]any); ok {
							c.add(key, leaf)
						}
					}
				}
			}
			c.walk(child, depth+1, key)
		}
	}
}

func isAttributionContainer(key string) bool {
	switch strings.ToLower(key) {
	case "references", "sourceattributions", "sourceattribution", "attributions", "attribution":
		return true
	default:
		return false
	}
}

func attributionLeaf(value map[string]any) bool {
	for _, key := range []string{"targetLink", "url", "source", "title", "displayData", "isCitedInResponse"} {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}

func (c *attributionCollector) add(kind string, value map[string]any) {
	if len(c.items) >= maxCanonicalAttributions {
		return
	}
	stringField := func(names ...string) string {
		for _, name := range names {
			if raw, ok := value[name]; ok {
				if text, ok := raw.(string); ok {
					return strings.TrimSpace(text)
				}
			}
		}
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	item := Attribution{
		Kind:              kind,
		ID:                stringField("id", "reference_id", "referenceId"),
		TargetLink:        stringField("targetLink", "url"),
		IsCitedInResponse: boolField(value, "isCitedInResponse"),
		Raw:               raw,
	}
	if display, ok := value["displayData"].(map[string]any); ok {
		item.DisplayData = display
	}
	identity := item.Kind + "\x00" + item.ID + "\x00" + item.TargetLink + "\x00" + string(raw)
	if _, duplicate := c.seen[identity]; duplicate {
		return
	}
	c.seen[identity] = struct{}{}
	c.items = append(c.items, item)
}

func boolField(value map[string]any, key string) bool {
	result, _ := value[key].(bool)
	return result
}

func CanonicalArtifactSummary(artifact Artifact) map[string]any {
	out := map[string]any{"kind": artifact.Kind}
	if artifact.Filename != "" {
		out["filename"] = artifact.Filename
	}
	if artifact.MIMEType != "" {
		out["mimeType"] = artifact.MIMEType
	}
	if artifact.PublicURL != "" {
		out["url"] = artifact.PublicURL
	}
	return out
}

func CanonicalAttributionSummary(attribution Attribution) map[string]any {
	out := map[string]any{"kind": attribution.Kind}
	if attribution.ID != "" {
		out["id"] = attribution.ID
	}
	if attribution.TargetLink != "" {
		out["targetLink"] = attribution.TargetLink
	}
	if attribution.IsCitedInResponse {
		out["isCitedInResponse"] = true
	}
	if attribution.DisplayData != nil {
		out["displayData"] = attribution.DisplayData
	}
	return out
}

func validateCanonicalArtifact(artifact Artifact) error {
	for name, value := range map[string]string{
		"referenceId":        artifact.ReferenceID,
		"filename":           artifact.Filename,
		"mimeType":           artifact.MIMEType,
		"codeResultImageUrl": artifact.CodeResultImageURL,
		"downloadUrl":        artifact.DownloadURL,
		"webUrl":             artifact.WebURL,
		"pollUrl":            artifact.PollURL,
		"fileToken":          artifact.FileToken,
	} {
		if len(value) > maxGeneratedArtifactURLBytes && strings.HasSuffix(strings.ToLower(name), "url") {
			return fmt.Errorf("%s exceeds canonical artifact limit", name)
		}
	}
	return nil
}

package web

import (
	"fmt"
	"strings"

	"m365-native/internal/chathub"
)

func parseContent(c any) (string, []chathub.Attachment) {
	var text strings.Builder
	var files []chathub.Attachment
	if s, ok := c.(string); ok {
		return s, nil
	}
	parts, ok := c.([]any)
	if !ok {
		return fmt.Sprint(c), nil
	}
	for _, raw := range parts {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if v, ok := m["text"].(string); ok && (typ == "text" || typ == "input_text" || typ == "output_text" || typ == "") {
			text.WriteString(v)
		}
		switch typ {
		case "text", "input_text", "output_text":
			// handled above
		case "image_url":
			a := chathub.Attachment{Type: "image", MimeType: "image/*"}
			switch image := m["image_url"].(type) {
			case string:
				a.URL = image
			case map[string]any:
				a.URL = stringValue(image, "url", "data", "image_url")
				a.Detail = stringValue(image, "detail", "image_detail")
			}
			if a.Detail == "" {
				a.Detail = stringValue(m, "detail", "image_detail")
			}
			if a.URL != "" {
				files = append(files, a)
			}
		case "input_image", "image":
			a := chathub.Attachment{Type: "image", MimeType: "image/*", Detail: stringValue(m, "detail", "image_detail")}
			a.URL = stringValue(m, "image_url", "url")
			if image, ok := m["image_url"].(map[string]any); ok {
				a.URL = stringValue(image, "url", "data", "image_url")
				if a.Detail == "" {
					a.Detail = stringValue(image, "detail", "image_detail")
				}
			}
			if source, ok := m["source"].(map[string]any); ok && a.URL == "" {
				a.URL = stringValue(source, "url", "data", "source")
			}
			if a.URL != "" {
				files = append(files, a)
			}
		case "input_file", "file":
			u := stringValue(m, "file_data", "file_url", "url", "source", "file_id")
			if u != "" || stringValue(m, "filename", "name") != "" {
				files = append(files, chathub.Attachment{Type: "file", URL: u, Name: stringValue(m, "filename", "name"), MimeType: stringValue(m, "mime_type", "mimeType", "content_type")})
			}
		case "input_audio", "audio":
			u := stringValue(m, "data", "audio_url", "url", "source")
			if u != "" {
				files = append(files, chathub.Attachment{Type: "audio", URL: u, MimeType: stringValue(m, "mime_type", "mimeType", "format", "content_type")})
			}
		}
	}
	return text.String(), files
}

func stringValue(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

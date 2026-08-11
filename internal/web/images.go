package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"m365-native/internal/chathub"
	"net/http"
	"strings"
	"time"
)

type imageGenerationRequest struct {
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
	Model          string `json:"model"`
	User           string `json:"user"`
}

func (s *Server) imageGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	settings := serverRuntimeSettings(s)
	bodyLimit, err := requestBodyLimit(settings.TextInputLimitUTF16)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}
	var b imageGenerationRequest
	if err := decodeBoundedJSON(w, r, bodyLimit, &b); err != nil {
		if isRequestBodyTooLarge(err) {
			writeRequestBodyTooLarge(w, r.URL.Path, bodyLimit)
			return
		}
		http.Error(w, `{"error":{"message":"prompt is required","type":"invalid_request_error"}}`, 400)
		return
	}
	if strings.TrimSpace(b.Prompt) == "" {
		http.Error(w, `{"error":{"message":"prompt is required","type":"invalid_request_error"}}`, 400)
		return
	}
	if err := validateCallerString(b.Prompt, settings.TextInputLimitUTF16); err != nil {
		writeOpenAIErrorCode(w, http.StatusBadRequest, "invalid_request_error", "text_policy_exceeded", err.Error())
		return
	}
	if b.N <= 0 {
		b.N = 1
	}
	if b.N > 4 {
		http.Error(w, "n must be between 1 and 4", 400)
		return
	}
	if b.ResponseFormat != "" && !strings.EqualFold(b.ResponseFormat, "url") && !strings.EqualFold(b.ResponseFormat, "b64_json") {
		http.Error(w, `{"error":{"message":"response_format must be url or b64_json","type":"invalid_request_error"}}`, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(settings.ImageTimeoutSeconds)*time.Second)
	defer cancel()
	acc, err := s.activeAccountContext(ctx)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if acc.OID == "" || acc.TID == "" {
		acc.OID, acc.TID = extractOIDTID(acc.AccessToken)
	}
	if acc.OID == "" || acc.TID == "" {
		http.Error(w, "account missing oid/tid", 400)
		return
	}
	size := b.Size
	if size == "" {
		size = "1024x1024"
	}
	prompt := fmt.Sprintf("Generate an image with the Flux model. Size: %s. Description: %s. Return the image URL directly.", size, b.Prompt)
	res, err := s.chat.Chat(ctx, chathub.Account{AccessToken: acc.AccessToken, OID: acc.OID, TID: acc.TID}, chathub.Request{Text: prompt, Tone: "magic"})
	if err != nil {
		if writeCanonicalTerminalError(w, err) {
			return
		}
		http.Error(w, upstreamError(err), 502)
		return
	}
	log.Printf("[image-gen] images=%d text_bytes=%d events=%d raw_bytes=%d conversation_present=%t", len(res.Images), len(res.Text), len(res.Events), len(res.RawResult), res.ConversationID != "")
	if len(res.Images) == 0 {
		// Fallback: try to find image URLs in the raw result
		if urls := extractImageURLs(res.RawResult); len(urls) > 0 {
			res.Images = urls
		}
	}
	if len(res.Images) == 0 {
		// Fallback: try to find image URLs in the response text
		if urls := extractImageURLs(res.Text); len(urls) > 0 {
			res.Images = urls
		}
	}
	if len(res.Images) == 0 {
		log.Printf("[image-gen] code=no_image_resource text_bytes=%d raw_bytes=%d events=%d", len(res.Text), len(res.RawResult), len(res.Events))
		http.Error(w, `{"error":{"message":"upstream returned no image resource","type":"upstream_error"}}`, 502)
		return
	}
	images := res.Images
	if len(images) > b.N {
		images = images[:b.N]
	}
	data := make([]map[string]string, 0, len(images))
	for _, u := range images {
		if strings.EqualFold(b.ResponseFormat, "b64_json") {
			if !strings.HasPrefix(u, "data:image/") {
				http.Error(w, `{"error":{"message":"upstream returned URL, not b64_json","type":"unsupported_response_format"}}`, 502)
				return
			}
			data = append(data, map[string]string{"b64_json": strings.SplitN(u, ",", 2)[1]})
		} else {
			data = append(data, map[string]string{"url": u})
		}
	}
	jsonOut(w, map[string]any{"created": time.Now().Unix(), "data": data, "m365": map[string]any{"conversationId": res.ConversationID, "sessionId": res.SessionID, "images": images}})
}

// extractImageURLs finds image URLs in a raw JSON string by searching for URL patterns.
func extractImageURLs(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				lk := strings.ToLower(k)
				if s, ok := e.(string); ok && (lk == "url" || lk == "imageurl" || lk == "thumbnailurl" || lk == "downloadurl" || lk == "src" || lk == "value" || lk == "data") {
					if strings.HasPrefix(s, "https://") && !chathub.ContainsProtectedArtifactReference(s) && !seen[s] {
						if strings.Contains(strings.ToLower(s), "image") || strings.HasSuffix(strings.ToLower(s), ".png") || strings.HasSuffix(strings.ToLower(s), ".jpg") || strings.HasSuffix(strings.ToLower(s), ".jpeg") || strings.HasSuffix(strings.ToLower(s), ".webp") || strings.HasSuffix(strings.ToLower(s), ".gif") {
							seen[s] = true
							out = append(out, s)
						}
					}
				} else {
					walk(e)
				}
			}
		}
	}
	walk(v)
	return out
}

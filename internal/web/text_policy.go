package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const (
	requestBodyOverhead        = int64(16 << 20)
	requestBytesPerUTF16       = int64(8)
	maximumTextInputLimitUTF16 = 6_291_456
	maxEncodedAttachmentBytes  = int64(((512 << 20) + 2) / 3 * 4)
	// The private HTTP guard accommodates the largest accepted request: three
	// inclusive 512 MiB attachments encoded as base64, the maximum text policy,
	// and bounded JSON/tool overhead. It remains an internal finite DoS guard.
	requestBodySafetyBytes = 3*maxEncodedAttachmentBytes +
		int64(maximumTextInputLimitUTF16)*requestBytesPerUTF16 + requestBodyOverhead
)

var errRequestBodyTooLarge = errors.New("Sidecar request body exceeds the internal resource-safety limit")

type callerTextLimitError struct {
	Units int
	Limit int
}

func (e *callerTextLimitError) Error() string {
	return fmt.Sprintf("caller text exceeds the configured Sidecar Web-compatibility policy of %d UTF-16 code units (received %d)", e.Limit, e.Units)
}

func utf16CodeUnits(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

func validateCallerString(text string, limit int) error {
	units := utf16CodeUnits(text)
	if units > limit {
		return &callerTextLimitError{Units: units, Limit: limit}
	}
	return nil
}

func validateCallerText(messages []oaiMsg, limit int) error {
	units := 0
	for _, message := range messages {
		if message.SidecarGenerated {
			continue
		}
		text, _ := parseContent(message.Content)
		units += utf16CodeUnits(text)
		if units > limit {
			return &callerTextLimitError{Units: units, Limit: limit}
		}
	}
	return nil
}

func requestBodyLimit(textLimitUTF16 int) (int64, error) {
	if textLimitUTF16 < 1 {
		return 0, fmt.Errorf("官方 Web 相容文字上限（UTF-16）必須大於 0")
	}
	if textLimitUTF16 > maximumTextInputLimitUTF16 {
		return 0, fmt.Errorf("官方 Web 相容文字上限（UTF-16）不得大於 %d，以維持有限的 Sidecar 資源保護", maximumTextInputLimitUTF16)
	}
	// The fixed ceiling also includes the complete active attachment envelope;
	// changing text policy never creates a smaller hidden file limit.
	return requestBodySafetyBytes, nil
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	if r.ContentLength > limit {
		return errRequestBodyTooLarge
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errRequestBodyTooLarge
		}
		return err
	}
	return ensureJSONEOF(decoder)
}

func isRequestBodyTooLarge(err error) bool {
	return errors.Is(err, errRequestBodyTooLarge)
}

func writeRequestBodyTooLarge(w http.ResponseWriter, path string, limit int64) {
	message := fmt.Sprintf("Sidecar request body exceeds the internal resource-safety limit of %d bytes", limit)
	switch path {
	case "/v1/responses":
		writeResponsesErrorCode(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", message)
	case "/v1/messages":
		writeAnthropicErrorCode(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", message)
	default:
		writeOpenAIErrorCode(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", message)
	}
}

type callerTextValidatedContextKey struct{}

func withCallerTextValidated(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerTextValidatedContextKey{}, true))
}

func callerTextAlreadyValidated(r *http.Request) bool {
	validated, _ := r.Context().Value(callerTextValidatedContextKey{}).(bool)
	return validated
}

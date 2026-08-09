package chathub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

func detectedImageMIME(file io.ReadSeeker) (string, error) {
	header := make([]byte, 16)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	header = header[:n]
	switch {
	case len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", nil
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "image/jpeg", nil
	case len(header) >= 6 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a"):
		return "image/gif", nil
	case len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "image/webp", nil
	default:
		return "", errors.New("image must be PNG, JPEG, GIF, or WebP")
	}
}

func compatibleImageMIME(claimed, detected string) bool {
	claimed = strings.ToLower(strings.TrimSpace(strings.Split(claimed, ";")[0]))
	if claimed == "" || claimed == "image/*" || claimed == "application/octet-stream" {
		return true
	}
	if claimed == "image/jpg" {
		claimed = "image/jpeg"
	}
	return claimed == detected
}

func (c *Client) imageMultipartBody(file io.ReadSeeker, mimeType, conversationID string) (io.ReadCloser, string) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	go func() {
		fail := func(err error) {
			_ = writer.CloseWithError(err)
		}
		if err := multipartWriter.WriteField("scenario", "UploadImage"); err != nil {
			fail(err)
			return
		}
		if err := multipartWriter.WriteField("conversationId", conversationID); err != nil {
			fail(err)
			return
		}
		part, err := multipartWriter.CreateFormField("FileBase64")
		if err != nil {
			fail(err)
			return
		}
		if _, err := io.WriteString(part, "data:"+mimeType+";base64,"); err != nil {
			fail(err)
			return
		}
		encoder := base64.NewEncoder(base64.StdEncoding, part)
		if _, err := io.Copy(encoder, file); err != nil {
			_ = encoder.Close()
			fail(err)
			return
		}
		if err := encoder.Close(); err != nil {
			fail(err)
			return
		}
		for _, option := range []string{"cwcgptvsan", "flux_v3_gptv_enable_upload_multi_image_in_turn_wo_ch", "gptvnorm2048"} {
			if err := multipartWriter.WriteField("optionsSets", option); err != nil {
				fail(err)
				return
			}
		}
		if err := multipartWriter.Close(); err != nil {
			fail(err)
			return
		}
		_ = writer.Close()
	}()
	return reader, contentType
}

func (c *Client) uploadImage(ctx context.Context, account Account, conversationID string, index int, attachment *Attachment) error {
	spool, err := c.spoolAttachment(ctx, attachment)
	if err != nil {
		return fmt.Errorf("image source: %w", err)
	}
	defer spool.close()
	mimeType, err := detectedImageMIME(spool.file)
	if err != nil {
		return fmt.Errorf("image source: %w", err)
	}
	if !compatibleImageMIME(spool.mimeType, mimeType) {
		return errors.New("image MIME type does not match its bytes")
	}
	if c.HTTPClient == nil {
		return errors.New("image upload HTTP client is unavailable")
	}
	body, contentType := c.imageMultipartBody(spool.file, mimeType, conversationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://substrate.office.com/m365Copilot/UploadFile", body)
	if err != nil {
		_ = body.Close()
		return err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+account.AccessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Variants", "feature.EnableImageSupportInUploadFile")
	request.Header.Set("X-Scenario", "OfficeWebIncludedCopilot")
	if account.OID != "" && account.TID != "" {
		request.Header.Set("X-AnchorMailbox", "Oid:"+account.OID+"@"+account.TID)
	}
	for key, values := range c.HTTPHeader {
		for _, value := range values {
			if key != "Origin" || value != "" {
				request.Header.Add(key, value)
			}
		}
	}
	if c.Trace != nil {
		c.Trace(map[string]any{"stage": "upload_start", "index": index, "attachment_size": spool.size, "token_present": account.AccessToken != "", "mime_type_present": mimeType != ""})
	}
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return errors.New("image upload request failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	_ = response.Body.Close()
	if readErr != nil {
		return errors.New("read image upload response failed")
	}
	if len(data) > 2<<20 {
		return errors.New("image upload response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("image upload returned HTTP %d", response.StatusCode)
	}
	var result struct {
		DocID    string `json:"docId"`
		FileName string `json:"fileName"`
		FileType string `json:"fileType"`
		Result   struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return errors.New("image upload returned invalid JSON")
	}
	if result.Result.Value != "Success" || strings.TrimSpace(result.DocID) == "" {
		return errors.New("image upload did not return a ready image")
	}
	fileType := strings.TrimPrefix(strings.ToLower(result.FileType), ".")
	if fileType == "jpeg" {
		fileType = "jpg"
	}
	if fileType == "" {
		fileType = strings.TrimPrefix(mimeType, "image/")
	}
	if attachment.Name == "" {
		attachment.Name = result.FileName
		if attachment.Name == "" {
			attachment.Name = spool.name
		}
	}
	attachment.OriginalName = attachment.Name
	attachment.DocID = result.DocID
	attachment.FileType = fileType
	attachment.MimeType = mimeType
	attachment.Size = spool.size
	attachment.UploadedConversationID = conversationID
	if c.Trace != nil {
		c.Trace(map[string]any{"stage": "upload_success", "index": index, "doc_id_present": true, "file_name_present": attachment.Name != "", "file_type_present": fileType != ""})
	}
	return nil
}

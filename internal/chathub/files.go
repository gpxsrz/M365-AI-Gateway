package chathub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	maxActiveAttachments     = 3
	maxAttachmentBytes       = int64(512 << 20)
	documentUploadChunkSize  = int64(983040) // 960 KiB, three Graph 320 KiB blocks.
	maxDocumentFilenameUTF16 = 293
)

var knownDocumentExtensions = map[string]struct{}{
	".txt": {}, ".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {},
	".ppt": {}, ".pptx": {}, ".csv": {}, ".json": {}, ".md": {}, ".html": {},
	".htm": {}, ".rtf": {}, ".xml": {}, ".yaml": {}, ".yml": {}, ".py": {},
	".js": {}, ".ts": {}, ".java": {}, ".c": {}, ".cc": {}, ".cpp": {},
	".cs": {}, ".go": {}, ".rs": {}, ".swift": {}, ".sql": {}, ".log": {},
}

type graphDriveItem struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	WebURL          string `json:"webUrl"`
	SPOID           string `json:"spoId"`
	ParentReference struct {
		DriveID string `json:"driveId"`
	} `json:"parentReference"`
}

func validateAttachmentSize(size int64) error {
	if size == 0 {
		return errors.New("attachment is empty")
	}
	if size < 0 || size > maxAttachmentBytes {
		return fmt.Errorf("attachment exceeds %d-byte limit", maxAttachmentBytes)
	}
	return nil
}

func utf16Units(value string) int {
	units := 0
	for _, r := range value {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

func truncateUTF16(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	units, end := 0, 0
	for index, r := range value {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > limit {
			break
		}
		units += width
		end = index + len(string(r))
	}
	return value[:end]
}

func safeDocumentName(original string) string {
	original = strings.ReplaceAll(strings.TrimSpace(original), `\`, "/")
	name := path.Base(original)
	if name == "." || name == "/" || name == "" {
		name = "attachment"
	}
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`"*:<>?/\|`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	name = strings.Trim(b.String(), " .")
	if name == "" {
		return "attachment"
	}
	return name
}

func documentTransportName(original, suffix string) (string, error) {
	name := safeDocumentName(original)
	ext := strings.ToLower(path.Ext(name))
	extensions := ext
	root := strings.TrimSuffix(name, ext)
	if _, ok := knownDocumentExtensions[ext]; !ok {
		extensions += ".txt"
	}
	if extensions == "" {
		extensions = ".txt"
	}
	suffix = strings.Trim(strings.TrimSpace(suffix), "-._ ")
	if suffix != "" {
		suffix = "-" + suffix
	}
	reserved := utf16Units(suffix + extensions)
	if reserved >= maxDocumentFilenameUTF16 {
		return "", errors.New("document filename suffix is too long")
	}
	if root == "" {
		root = "file"
	}
	root = truncateUTF16(root, maxDocumentFilenameUTF16-reserved)
	if root == "" {
		root = truncateUTF16("file", maxDocumentFilenameUTF16-reserved)
	}
	return root + suffix + extensions, nil
}

func randomAttachmentSuffix() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate attachment filename identity: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (c *Client) attachmentNameSuffix() (string, error) {
	if c.AttachmentNameSuffix != nil {
		return c.AttachmentNameSuffix()
	}
	return randomAttachmentSuffix()
}

type spooledAttachment struct {
	file     *os.File
	size     int64
	mimeType string
	name     string
}

func (s *spooledAttachment) close() {
	if s == nil || s.file == nil {
		return
	}
	name := s.file.Name()
	_ = s.file.Close()
	_ = os.Remove(name)
}

func copyAttachment(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > maxAttachmentBytes {
				return total, validateAttachmentSize(total)
			}
			if _, err := dst.Write(buffer[:n]); err != nil {
				return total, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return total, readErr
		}
	}
	return total, validateAttachmentSize(total)
}

func parseAttachmentDataURL(raw string) (io.Reader, string, error) {
	comma := strings.IndexByte(raw, ',')
	if comma < len("data:") {
		return nil, "", errors.New("invalid attachment data URL")
	}
	header := raw[len("data:"):comma]
	parts := strings.Split(header, ";")
	if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-1], "base64") {
		return nil, "", errors.New("attachment data URL must be base64")
	}
	mimeType := strings.TrimSpace(parts[0])
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return base64.NewDecoder(base64.StdEncoding, strings.NewReader(raw[comma+1:])), mimeType, nil
}

func (c *Client) spoolAttachment(ctx context.Context, attachment *Attachment) (*spooledAttachment, error) {
	file, err := os.CreateTemp("", ".m365-attachment-*")
	if err != nil {
		return nil, fmt.Errorf("create private attachment spool: %w", err)
	}
	spool := &spooledAttachment{file: file, mimeType: attachment.MimeType, name: attachment.Name}
	remove := true
	defer func() {
		if remove {
			spool.close()
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("secure attachment spool: %w", err)
	}

	var source io.ReadCloser
	if strings.HasPrefix(strings.ToLower(attachment.URL), "data:") {
		reader, mimeType, err := parseAttachmentDataURL(attachment.URL)
		if err != nil {
			return nil, err
		}
		if spool.mimeType == "" {
			spool.mimeType = mimeType
		}
		source = io.NopCloser(reader)
	} else {
		response, finalURL, err := c.downloadRemoteAttachment(ctx, attachment.URL)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return nil, fmt.Errorf("attachment download returned HTTP %d", response.StatusCode)
		}
		if response.ContentLength > maxAttachmentBytes {
			_ = response.Body.Close()
			return nil, validateAttachmentSize(response.ContentLength)
		}
		if spool.mimeType == "" {
			spool.mimeType = strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
		}
		if spool.name == "" {
			spool.name = path.Base(finalURL.Path)
		}
		source = response.Body
	}
	if spool.name == "" {
		spool.name = "attachment"
	}
	defer source.Close()

	spool.size, err = copyAttachment(ctx, file, source)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind attachment spool: %w", err)
	}
	remove = false
	return spool, nil
}

func (c *Client) uploadDocument(ctx context.Context, account Account, conversationID string, attachment *Attachment) error {
	if strings.TrimSpace(account.GraphAccessToken) == "" {
		return errors.New("document upload requires a Microsoft Graph access token")
	}
	spool, err := c.spoolAttachment(ctx, attachment)
	if err != nil {
		return fmt.Errorf("document source: %w", err)
	}
	defer spool.close()
	suffix, err := c.attachmentNameSuffix()
	if err != nil {
		return err
	}
	transportName, err := documentTransportName(spool.name, suffix)
	if err != nil {
		return err
	}
	item, err := c.uploadGraphDocument(ctx, account.GraphAccessToken, spool.file, spool.size, transportName)
	if err != nil {
		return fmt.Errorf("document upload: %w", err)
	}
	localID := strings.TrimSpace(item.SPOID)
	if localID == "" {
		localID, err = deriveLocalFileID(item.ID, item.ParentReference.DriveID)
		if err != nil {
			return fmt.Errorf("document identity: %w", err)
		}
	}
	if parsed, err := url.Parse(item.WebURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("document upload returned an invalid reference URL")
	}
	attachment.OriginalName = spool.name
	attachment.TransportName = transportName
	attachment.DocID = localID
	attachment.ReferenceURL = item.WebURL
	attachment.UploadedConversationID = conversationID
	attachment.Size = spool.size
	return nil
}

func graphUploadSessionURL(name string) string {
	return "https://graph.microsoft.com/v1.0/me/drive/special/copilotuploads:/" + url.PathEscape(name) + ":/createUploadSession"
}

func validUploadSessionURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".sharepoint.com") || strings.HasSuffix(host, ".sharepoint-df.com")
}

func (c *Client) uploadGraphDocument(ctx context.Context, graphToken string, file *os.File, size int64, transportName string) (graphDriveItem, error) {
	body, err := json.Marshal(map[string]any{"item": map[string]any{
		"@microsoft.graph.conflictBehavior": "replace",
		"name":                              transportName,
	}})
	if err != nil {
		return graphDriveItem{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, graphUploadSessionURL(transportName), strings.NewReader(string(body)))
	if err != nil {
		return graphDriveItem{}, err
	}
	request.Header.Set("Authorization", "Bearer "+graphToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return graphDriveItem{}, errors.New("create upload session failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil {
		return graphDriveItem{}, errors.New("read upload session response failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return graphDriveItem{}, fmt.Errorf("create upload session returned HTTP %d", response.StatusCode)
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(data, &session); err != nil || !validUploadSessionURL(session.UploadURL) {
		return graphDriveItem{}, errors.New("create upload session returned an invalid upload URL")
	}

	cancelSession := true
	defer func() {
		if !cancelSession {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanup, err := http.NewRequestWithContext(cleanupContext, http.MethodDelete, session.UploadURL, nil)
		if err == nil {
			if cleanupResponse, err := c.HTTPClient.Do(cleanup); err == nil && cleanupResponse.Body != nil {
				_ = cleanupResponse.Body.Close()
			}
		}
	}()

	var ready graphDriveItem
	for offset := int64(0); offset < size; offset += documentUploadChunkSize {
		length := min64(documentUploadChunkSize, size-offset)
		part := io.NewSectionReader(file, offset, length)
		put, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, part)
		if err != nil {
			return graphDriveItem{}, err
		}
		put.ContentLength = length
		put.Header.Set("Content-Type", "application/octet-stream")
		put.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size))
		putResponse, err := c.HTTPClient.Do(put)
		if err != nil {
			return graphDriveItem{}, errors.New("upload session PUT failed")
		}
		putData, readErr := io.ReadAll(io.LimitReader(putResponse.Body, 1<<20))
		_ = putResponse.Body.Close()
		if readErr != nil {
			return graphDriveItem{}, errors.New("read upload PUT response failed")
		}
		if putResponse.StatusCode < 200 || putResponse.StatusCode >= 300 {
			return graphDriveItem{}, fmt.Errorf("upload session PUT returned HTTP %d", putResponse.StatusCode)
		}
		if offset+length == size {
			if err := json.Unmarshal(putData, &ready); err != nil || strings.TrimSpace(ready.ID) == "" {
				return graphDriveItem{}, errors.New("final upload response did not contain a ready DriveItem")
			}
		}
	}
	cancelSession = false
	return ready, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func deriveLocalFileID(itemID, driveID string) (string, error) {
	itemID = strings.TrimSpace(itemID)
	driveID = strings.TrimSpace(driveID)
	if itemID == "" || len(driveID) < 3 {
		return "", errors.New("DriveItem identity is incomplete")
	}
	raw, err := base64.RawURLEncoding.DecodeString(driveID[2:])
	if err != nil || len(raw) < 48 {
		return "", errors.New("DriveItem drive identity is invalid")
	}
	guids := make([]string, 3)
	for i := range guids {
		guids[i] = microsoftGUID(raw[i*16 : (i+1)*16])
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(guids, ",")))
	return "SPO_" + encoded + "_" + itemID, nil
}

func microsoftGUID(raw []byte) string {
	ordered := []byte{raw[3], raw[2], raw[1], raw[0], raw[5], raw[4], raw[7], raw[6], raw[8], raw[9], raw[10], raw[11], raw[12], raw[13], raw[14], raw[15]}
	hexValue := hex.EncodeToString(ordered)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:]
}

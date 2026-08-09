package web

import (
	"context"
	"errors"
	"io"
	"m365-native/internal/chathub"
	"m365-native/internal/outbound"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var errArtifactMaterialization = errors.New("generated artifact could not be made downloadable")

func configuredArtifactStoreRoot() string {
	if dataDir := strings.TrimSpace(os.Getenv("M365_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "artifacts")
	}
	return filepath.Join(filepath.Dir(settingsPath()), "artifacts")
}

func (s *Server) configureArtifactService() error {
	store, err := openArtifactStore(configuredArtifactStoreRoot(), artifactStoreOptions{})
	if err != nil {
		return err
	}
	s.artifacts = store
	s.artifactFetch = &artifactFetchClient{
		HTTPClient:    outbound.HTTPClient(),
		Token:         s.resourceAccessToken,
		Invalidate:    s.invalidateResourceAccessToken,
		ClientVersion: "M365-Copilot2API/" + strings.TrimSpace(Version),
	}
	return nil
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	mcpRuntime := s.mcp
	s.mu.Unlock()
	if mcpRuntime != nil {
		mcpRuntime.Close()
	}
	if s.artifacts != nil {
		return s.artifacts.Close()
	}
	return nil
}

// materializeArtifacts is the sole conversion boundary from protected
// Microsoft artifact metadata to caller-visible Sidecar download links.
// It returns only the newly appended Markdown so streaming adapters can emit it
// after retrieval has succeeded and before their success terminal.
func (s *Server) materializeArtifacts(ctx context.Context, request *http.Request, result *chathub.Result) (string, error) {
	if result == nil {
		return "", nil
	}
	if err := chathub.CanonicalizeResult(result); err != nil {
		return "", errArtifactMaterialization
	}
	type fetchableArtifact struct {
		index       int
		upstreamURL string
	}
	generated := make([]fetchableArtifact, 0, len(result.Artifacts))
	for index, artifact := range result.Artifacts {
		upstreamURL := strings.TrimSpace(artifact.CodeResultFileURL)
		if upstreamURL == "" {
			upstreamURL = strings.TrimSpace(artifact.CodeResultImageURL)
		}
		if upstreamURL == "" && strings.TrimSpace(artifact.DownloadURL) != "" {
			if _, parseErr := parseArtifactLocation(artifact.DownloadURL); parseErr == nil {
				upstreamURL = artifact.DownloadURL
			}
		}
		if upstreamURL != "" {
			generated = append(generated, fetchableArtifact{index: index, upstreamURL: upstreamURL})
		}
	}
	if len(generated) == 0 {
		if chathub.ContainsProtectedArtifactReference(result.Text) {
			return "", errArtifactMaterialization
		}
		return "", nil
	}
	if s.artifacts == nil || s.artifactFetch == nil || request == nil {
		return "", errArtifactMaterialization
	}
	origin, err := s.artifactPublicOrigin(request)
	if err != nil {
		return "", errArtifactMaterialization
	}

	created := make([]artifactRecord, 0, len(generated))
	rollback := func() {
		for _, record := range created {
			_ = s.artifacts.Delete(record.Token)
		}
	}
	links := make([]string, 0, len(generated))
	for _, candidate := range generated {
		artifact := &result.Artifacts[candidate.index]
		fetched, fetchErr := s.artifactFetch.Fetch(ctx, candidate.upstreamURL, artifact.Filename)
		if fetchErr != nil {
			rollback()
			return "", errArtifactMaterialization
		}
		putLimit := fetched.MaxBytes
		if fetched.ContentLength > 0 {
			putLimit = fetched.ContentLength
		}
		record, storeErr := s.artifacts.PutReader(fetched.Filename, fetched.Body, putLimit)
		closeErr := fetched.Body.Close()
		if storeErr != nil || closeErr != nil || record.Size == 0 || (fetched.ContentLength >= 0 && record.Size != fetched.ContentLength) {
			if storeErr == nil {
				_ = s.artifacts.Delete(record.Token)
			}
			rollback()
			return "", errArtifactMaterialization
		}
		created = append(created, record)
		downloadURL := origin + artifactRoutePrefix + record.Token + artifactRouteSuffix
		result.Text = strings.ReplaceAll(result.Text, candidate.upstreamURL, downloadURL)
		artifact.PublicURL = downloadURL
		artifact.Filename = record.Filename
		canonicalLink := "[下載 " + markdownArtifactLabel(record.Filename) + "](" + downloadURL + ")"
		if !strings.Contains(result.Text, canonicalLink) {
			links = append(links, canonicalLink)
		}
	}
	if chathub.ContainsProtectedArtifactReference(result.Text) {
		rollback()
		return "", errArtifactMaterialization
	}
	if len(links) == 0 {
		return "", nil
	}
	markdown := strings.Join(links, "\n")
	if strings.TrimSpace(result.Text) == "" {
		result.Text = markdown
		return markdown, nil
	}
	markdown = "\n\n" + markdown
	result.Text = strings.TrimRight(result.Text, " \t\r\n") + markdown
	return markdown, nil
}

func markdownArtifactLabel(filename string) string {
	filename = strings.Join(strings.Fields(filename), " ")
	filename = strings.ReplaceAll(filename, `\`, `\\`)
	filename = strings.ReplaceAll(filename, "[", `\[`)
	filename = strings.ReplaceAll(filename, "]", `\]`)
	return filename
}

func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request) {
	token, valid := artifactCapabilityToken(r.URL.Path)
	if !valid || s.artifacts == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var record artifactRecord
	var file *os.File
	var err error
	if r.Method == http.MethodHead {
		record, err = s.artifacts.Stat(token)
	} else {
		record, file, err = s.artifacts.Open(token)
		if file != nil {
			defer file.Close()
		}
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": record.Filename})
	if disposition == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(record.Size, 10))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if file != nil {
		_, _ = io.Copy(w, file)
	}
}

package web

import (
	"context"
	"errors"
	"fmt"
	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const graphResourceScope = "https://graph.microsoft.com/.default openid profile offline_access"

func validateActiveAttachments(attachments []chathub.Attachment) error {
	if len(attachments) > 3 {
		return errors.New("active attachments exceed the shared limit of 3")
	}
	for _, attachment := range attachments {
		switch attachment.Type {
		case "image", "file":
		default:
			return fmt.Errorf("unsupported attachment type %q", attachment.Type)
		}
		raw := strings.TrimSpace(attachment.URL)
		if raw == "" {
			return errors.New("attachment source is required")
		}
		if strings.HasPrefix(strings.ToLower(raw), "data:") {
			if comma := strings.IndexByte(raw, ','); comma < 0 || comma == len(raw)-1 {
				return errors.New("attachment data is empty")
			}
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("attachment source must be a base64 data URL or public HTTPS URL; unresolved file_id is unsupported")
		}
	}
	return nil
}

func normalizeCompatibilityParameters(attachments []chathub.Attachment, verbosity string) ([]string, error) {
	if err := validateActiveAttachments(attachments); err != nil {
		return nil, err
	}
	downgraded := map[string]struct{}{}
	for i := range attachments {
		detail := strings.TrimSpace(attachments[i].Detail)
		if detail == "" {
			continue
		}
		switch detail {
		case "auto", "low", "high", "original":
			downgraded["image_detail"] = struct{}{}
			attachments[i].Detail = ""
		default:
			return nil, errors.New("image detail must be one of auto, low, high, or original")
		}
	}
	verbosity = strings.TrimSpace(verbosity)
	if verbosity != "" {
		switch verbosity {
		case "low", "medium", "high":
			downgraded["verbosity"] = struct{}{}
		default:
			return nil, errors.New("verbosity must be one of low, medium, or high")
		}
	}
	names := make([]string, 0, len(downgraded))
	for name := range downgraded {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func adapterCompatibilityParameters(request oaiReq) ([]string, error) {
	attachments := append([]chathub.Attachment(nil), request.Attachments...)
	for _, message := range request.Messages {
		_, parsed := parseContent(message.Content)
		attachments = append(attachments, parsed...)
	}
	// This pre-check sees the caller's full active history. Validate accepted
	// downgrade values here, but apply the active-three quota only after the
	// checkpoint has reduced the request to its outbound delta.
	downgraded := map[string]struct{}{}
	for _, attachment := range attachments {
		detail := strings.TrimSpace(attachment.Detail)
		if detail == "" {
			continue
		}
		switch detail {
		case "auto", "low", "high", "original":
			downgraded["image_detail"] = struct{}{}
		default:
			return nil, errors.New("image detail must be one of auto, low, high, or original")
		}
	}
	verbosity := strings.TrimSpace(request.Verbosity)
	if verbosity != "" {
		switch verbosity {
		case "low", "medium", "high":
			downgraded["verbosity"] = struct{}{}
		default:
			return nil, errors.New("verbosity must be one of low, medium, or high")
		}
	}
	names := make([]string, 0, len(downgraded))
	for name := range downgraded {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func setDowngradedParameters(w http.ResponseWriter, names []string) {
	if len(names) == 0 {
		return
	}
	w.Header().Set("X-M365-Downgraded-Parameters", strings.Join(names, ","))
	exposed := map[string]struct{}{}
	for _, value := range strings.Split(w.Header().Get("Access-Control-Expose-Headers"), ",") {
		if value = strings.TrimSpace(value); value != "" {
			exposed[value] = struct{}{}
		}
	}
	exposed["X-M365-Downgraded-Parameters"] = struct{}{}
	values := make([]string, 0, len(exposed))
	for value := range exposed {
		values = append(values, value)
	}
	sort.Strings(values)
	w.Header().Set("Access-Control-Expose-Headers", strings.Join(values, ", "))
}

func hasDocumentAttachment(attachments []chathub.Attachment) bool {
	for _, attachment := range attachments {
		if attachment.Type == "file" {
			return true
		}
	}
	return false
}

func (s *Server) resourceAccessToken(ctx context.Context, scope string) (string, error) {
	if s.resourceToken != nil {
		return s.resourceToken(ctx, scope)
	}
	store := s.activeTokenStore()
	if store == nil {
		return "", errors.New("account token store is unavailable")
	}
	return store.ResourceAccessToken(ctx, scope)
}

func (s *Server) invalidateResourceAccessToken(scope, rejectedToken string) {
	if s.resourceInvalidate != nil {
		s.resourceInvalidate(scope, rejectedToken)
		return
	}
	if store := s.activeTokenStore(); store != nil {
		store.InvalidateResourceAccessToken(scope, rejectedToken)
	}
}

func (s *Server) chatHubAccount(ctx context.Context, account auth.AccountToken, attachments []chathub.Attachment) (chathub.Account, error) {
	result := chathub.Account{AccessToken: account.AccessToken, OID: account.OID, TID: account.TID}
	if hasDocumentAttachment(attachments) {
		graphToken, err := s.resourceAccessToken(ctx, graphResourceScope)
		if err != nil {
			return chathub.Account{}, fmt.Errorf("Microsoft Graph authorization unavailable: %w", err)
		}
		result.GraphAccessToken = graphToken
	}
	return result, nil
}

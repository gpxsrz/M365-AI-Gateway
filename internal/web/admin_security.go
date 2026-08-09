package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type adminCredentialMode uint8

const (
	adminCredentialUnavailable adminCredentialMode = iota
	adminCredentialPersisted
	adminCredentialBootstrap
	adminCredentialBootstrapConsumed
)

var (
	errAdminCredentialUnavailable = errors.New("管理員憑證無法使用")
	errAdminCredentialChanged     = errors.New("管理員憑證已變更")
	errAdminBootstrapConsumed     = errors.New("管理員一次性 bootstrap secret 已使用")
)

type adminCredential struct {
	Password string
	Mode     adminCredentialMode
}

type loginAttempt struct {
	Failures                 int
	WindowStart, LockedUntil time.Time
}

func adminPasswordPath() string {
	if p := strings.TrimSpace(os.Getenv("M365_ADMIN_PASSWORD_FILE")); p != "" {
		return p
	}
	if dir := strings.TrimSpace(os.Getenv("M365_DATA_DIR")); dir != "" {
		return filepath.Join(dir, "admin-password")
	}
	if p := strings.TrimSpace(os.Getenv("M365_CONFIG")); p != "" {
		return filepath.Join(filepath.Dir(p), "admin-password")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "m365-native", "admin-password")
}

func adminBootstrapConsumedPath() string {
	return adminPasswordPath() + ".bootstrap-consumed"
}

func loadAdminCredential() (adminCredential, error) {
	// A present persisted file is authoritative, including an empty/corrupt
	// file, so bootstrap material can never silently resurrect behind it.
	b, err := os.ReadFile(adminPasswordPath())
	if err == nil {
		password := strings.TrimSpace(string(b))
		if password == "" {
			return adminCredential{}, nil
		}
		return adminCredential{Password: password, Mode: adminCredentialPersisted}, nil
	}
	if !os.IsNotExist(err) {
		return adminCredential{}, fmt.Errorf("read persisted administrator credential: %w", err)
	}

	if _, err := os.Stat(adminBootstrapConsumedPath()); err == nil {
		return adminCredential{}, nil
	} else if !os.IsNotExist(err) {
		return adminCredential{}, fmt.Errorf("read administrator bootstrap state: %w", err)
	}

	if bootstrapPath := strings.TrimSpace(os.Getenv("M365_ADMIN_PASSWORD_BOOTSTRAP_FILE")); bootstrapPath != "" {
		b, err := os.ReadFile(bootstrapPath)
		if err != nil {
			// Bootstrap is optional input, not authority. Missing, unreadable, or
			// incorrectly mounted input leaves management safely unavailable.
			return adminCredential{}, nil
		}
		password := strings.TrimSpace(string(b))
		if password == "" {
			return adminCredential{}, nil
		}
		return adminCredential{Password: password, Mode: adminCredentialBootstrap}, nil
	}

	if password := strings.TrimSpace(os.Getenv("M365_ADMIN_PASSWORD")); password != "" {
		return adminCredential{Password: password, Mode: adminCredentialBootstrap}, nil
	}
	return adminCredential{}, nil
}

func saveAdminPassword(password string) error {
	p := adminPasswordPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".admin-password-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(password + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// os.Rename replaces an existing regular destination on supported Go
	// platforms; on Windows the standard library uses MoveFileEx with
	// MOVEFILE_REPLACE_EXISTING.
	return os.Rename(temporary, p)
}

func markAdminBootstrapConsumed() error {
	p := adminBootstrapConsumedPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return errAdminBootstrapConsumed
	}
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = f.Close()
		if remove {
			_ = os.Remove(p)
		}
	}()
	if _, err := f.WriteString("m365-admin-bootstrap-consumed-v1\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func validNewAdminPassword(p string) error {
	if len(p) < 6 {
		return errors.New("新密碼至少需要 6 個字元")
	}
	if len(p) > 256 {
		return errors.New("新密碼過長")
	}
	return nil
}

func (s *Server) establishAdminSession(expectedPassword string, expectedMode adminCredentialMode, token string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adminCredentialMode != expectedMode || subtle.ConstantTimeCompare([]byte(s.adminPassword), []byte(expectedPassword)) != 1 {
		if expectedMode == adminCredentialBootstrap && s.adminCredentialMode != adminCredentialBootstrap {
			return false, errAdminBootstrapConsumed
		}
		return false, errAdminCredentialChanged
	}
	switch expectedMode {
	case adminCredentialPersisted:
		// Normal persisted login.
	case adminCredentialBootstrap:
		if err := markAdminBootstrapConsumed(); err != nil {
			if errors.Is(err, errAdminBootstrapConsumed) {
				s.adminPassword = ""
				s.adminCredentialMode = adminCredentialUnavailable
				s.mustChangePassword = false
				s.adminSessions = map[string]adminSession{}
			}
			return false, err
		}
		s.adminCredentialMode = adminCredentialBootstrapConsumed
		s.mustChangePassword = true
		// The newly issued session is the only session allowed to rotate the
		// consumed bootstrap credential.
		s.adminSessions = map[string]adminSession{}
	default:
		return false, errAdminCredentialUnavailable
	}
	s.adminSessions[token] = adminSession{CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(adminSessionAbsoluteTimeout)}
	return s.mustChangePassword, nil
}

func (s *Server) loginAllowed(ip string, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.loginAttempts[ip]
	if now.Before(a.LockedUntil) {
		return false, time.Until(a.LockedUntil)
	}
	if a.WindowStart.IsZero() || now.Sub(a.WindowStart) > 15*time.Minute {
		delete(s.loginAttempts, ip)
	}
	return true, 0
}

const maxLoginAttemptEntries = 4096

func (s *Server) recordLoginFailure(ip string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.loginAttempts[ip]; !exists && len(s.loginAttempts) >= maxLoginAttemptEntries {
		for key, attempt := range s.loginAttempts {
			if now.Sub(attempt.WindowStart) > 15*time.Minute && now.After(attempt.LockedUntil) {
				delete(s.loginAttempts, key)
			}
		}
		if len(s.loginAttempts) >= maxLoginAttemptEntries {
			return
		}
	}
	a := s.loginAttempts[ip]
	if a.WindowStart.IsZero() || now.Sub(a.WindowStart) > 15*time.Minute {
		a = loginAttempt{WindowStart: now}
	}
	a.Failures++
	if a.Failures >= 5 {
		a.LockedUntil = now.Add(15 * time.Minute)
	}
	s.loginAttempts[ip] = a
}

func (s *Server) clearLoginFailures(ip string) {
	s.mu.Lock()
	delete(s.loginAttempts, ip)
	s.mu.Unlock()
}

func (s *Server) adminChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, 405, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	if !s.validAdminSession(r) {
		writeOpenAIError(w, 401, "auth_error", "需要先以管理員身分登入")
		return
	}
	var b struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b) != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "JSON 格式錯誤")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.adminPassword
	if current == "" || s.adminCredentialMode == adminCredentialUnavailable {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "管理員憑證無法使用")
		return
	}
	if subtle.ConstantTimeCompare([]byte(b.Current), []byte(current)) != 1 {
		writeOpenAIError(w, 401, "auth_error", "目前密碼不正確")
		return
	}
	if subtle.ConstantTimeCompare([]byte(b.New), []byte(current)) == 1 {
		writeOpenAIError(w, 400, "invalid_request_error", "新密碼不得與目前密碼相同")
		return
	}
	if err := validNewAdminPassword(b.New); err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", err.Error())
		return
	}
	if err := saveAdminPassword(b.New); err != nil {
		writeOpenAIError(w, 500, "storage_error", "無法儲存管理員密碼；請檢查持久化資料目錄的權限")
		return
	}
	s.adminPassword = b.New
	s.adminCredentialMode = adminCredentialPersisted
	s.mustChangePassword = false
	s.adminSessions = map[string]adminSession{}
	http.SetCookie(w, &http.Cookie{Name: "m365_admin_session", Path: "/", HttpOnly: true, Secure: secureAdminCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	jsonOut(w, map[string]any{"status": "password_changed", "reauthenticate": true})
}

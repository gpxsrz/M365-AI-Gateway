package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var forbiddenSimplifiedAdministrationTerms = []string{
	"登录", "管理员", "账号", "添加", "访问配置", "运行日志", "设置", "调试", "请求失败",
	"加载", "刷新", "检查更新", "在线", "离线", "令牌", "撤销", "删除", "代理池", "搭建",
	"检测", "轮换", "冷却", "批量", "节点", "默认", "输入", "输出", "缓存", "响应", "网关",
	"浏览器", "粘贴", "链接", "超时", "生成", "配置", "当前", "开发版",
	"稳定版", "接口", "权限", "创建", "复制", "剪贴板", "密码管理器", "失败次数", "稍后", "支持",
	"会话", "凭据", "两次输入", "字符", "修改成功", "必须", "保护",
	"状态", "时间", "与", "切换", "会", "并", "优先", "用于", "记录", "建议", "这个", "一个",
	"识别", "尝试", "将", "发现", "个代理", "填写", "字段", "轮", "数", "项", "服务", "参数", "逗号",
}

func TestAdministrationTemplatesAreTaiwanTraditionalChinese(t *testing.T) {
	for _, path := range []string{"web/login.html", "web/index.html", "web/debug.html"} {
		rawPath := filepath.Join("../..", path)
		source, err := os.ReadFile(rawPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(source), `lang="zh-TW"`) {
			t.Errorf("%s source does not declare zh-TW", path)
		}
		for _, term := range forbiddenSimplifiedAdministrationTerms {
			if strings.Contains(string(source), term) {
				t.Errorf("%s source contains simplified administration term %q", path, term)
			}
		}
	}
}

func TestAdministrationTemplatesUseTaiwanTerminology(t *testing.T) {
	expected := map[string][]string{
		"web/login.html": {"管理員登入", "管理主控台", "Microsoft 帳號", "切換密碼顯示"},
		"web/index.html": {"Microsoft 帳號", "登入帳號", "存取設定", "執行日誌", "代理集區", "設定"},
		"web/debug.html": {"診斷摘要", "短期診斷快照", "純量內容", "請求標頭", "重新整理", "返回管理介面", "協定", "路由", "請求 ID", "快照到期時間"},
	}
	forbidden := map[string][]string{
		"web/login.html": {"帳號集區"},
		"web/index.html": {"帳號集區", "新增帳號", "profileRef", "profileRefVersion", "/api/accounts"},
	}
	for path, phrases := range expected {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, phrase := range phrases {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s missing Taiwan terminology %q", path, phrase)
			}
		}
		for _, phrase := range forbidden[path] {
			if strings.Contains(text, phrase) {
				t.Errorf("%s contains removed multi-account surface %q", path, phrase)
			}
		}
		if path == "web/debug.html" {
			for _, forbidden := range []string{"scalar 正文", "request headers", "Snapshot expires", "Request ID", ">Protocol<", ">Route<"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s contains untranslated label %q", path, forbidden)
				}
			}
		}
	}
}

func TestAdministrationTemplateHasNoUnusedLegacyAssets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("../..", "web/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, removed := range []string{
		"chart.js", `id="login"`, "@keyframes scaleIn", "@keyframes spin", "@keyframes pulse",
		".account-cell{", ".avatar{",
	} {
		if strings.Contains(text, removed) {
			t.Errorf("management UI still contains unused legacy asset %q", removed)
		}
	}
}

func TestWP6ManagementSettingsExposeProductPolicyOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("../..", "web/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"聊天模式", "Private / Temporary Chat", "Normal Chat",
		"官方 Web 相容文字上限（UTF-16）", "128000", "進階 / 實驗性",
		"大幅增加處理時間或逾時", "聊天逾時（秒）",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("management UI missing WP6 setting %q", required)
		}
	}
	for _, forbidden := range []string{"disableMemory", "10 MiB", "serialized JSON bytes", "WebSocket frame bytes"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("management UI exposes internal control %q", forbidden)
		}
	}
}

func TestAdministrationTemplatesUseExplicitTaipeiDateFormatting(t *testing.T) {
	for _, path := range []string{"web/index.html", "web/debug.html"} {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, `Intl.DateTimeFormat('zh-TW'`) || !strings.Contains(text, `timeZone:'Asia/Taipei'`) {
			t.Errorf("%s does not use explicit zh-TW / Asia/Taipei formatting", path)
		}
		if strings.Contains(text, ".toLocaleString()") {
			t.Errorf("%s still uses browser-default locale/timezone formatting", path)
		}
	}
}

var forbiddenEnglishManagementMessages = []string{
	"method not allowed", "administrator login required", "bad json", "administrator credential is unavailable",
	"current password is invalid", "new password must differ from the current password",
	"administrator password could not be saved", "too many failed login attempts", "invalid administrator password",
	"session failure", "administrator bootstrap could not be consumed", "key not found",
	"account profile references are unavailable", "valid account profile reference required", "account profile not found",
	"account deleted but profile reference retirement requires recovery", "pkce failure", "state failure",
	"missing state", "missing state or code", "invalid or expired state", "account profile reference unavailable",
	"request body too large", "debug record not found", "ttlSeconds must be positive",
	"unable to start diagnostic session", "unable to clear diagnostic session",
	"non-loopback administration is unavailable while bootstrap credentials are active",
	"request peer address is invalid", "non-loopback administration requires HTTPS", "management host is not allowlisted",
	"one Origin header is required", "Origin is invalid", "Origin scheme is invalid", "Origin host is invalid",
	"Origin does not match the management host", "conversation not found", "deployment not found",
}

func TestVisibleManagementGoSurfacesContainNoSimplifiedAdministrationTerms(t *testing.T) {
	paths := []string{
		"server.go", "settings.go", "version.go", "deployments.go", "proxy_pool.go", "keys.go", "conversations.go",
		"admin_security.go", "admin_security_bootstrap.go", "admin_request_security.go", "security_http.go", "debug.go", "oauth_callback.go", "oauth_profile_lifecycle.go",
	}
	for _, name := range paths {
		raw, err := os.ReadFile(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		text := string(raw)
		if name == "server.go" {
			if boundary := strings.Index(text, "type chatBody struct"); boundary >= 0 {
				text = text[:boundary]
			}
		}
		if name == "debug.go" {
			if boundary := strings.Index(text, "func (server *Server) debugList"); boundary >= 0 {
				text = text[boundary:]
			}
		}
		for _, term := range forbiddenSimplifiedAdministrationTerms {
			if strings.Contains(text, term) {
				t.Errorf("%s contains simplified visible term %q", name, term)
			}
		}
		for _, message := range forbiddenEnglishManagementMessages {
			if strings.Contains(text, message) {
				t.Errorf("%s contains English visible management message %q", name, message)
			}
		}
	}
}

func TestOAuthCompletionTemplateIsZhTW(t *testing.T) {
	rr := httptest.NewRecorder()
	writeOAuthCompletionPage(rr)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, body)
	}
	for _, required := range []string{`lang="zh-TW"`, "授權完成", "Microsoft 帳號已登入", "關閉此頁面"} {
		if !strings.Contains(body, required) {
			t.Errorf("OAuth completion page missing %q: %s", required, body)
		}
	}
	for _, term := range forbiddenSimplifiedAdministrationTerms {
		if strings.Contains(body, term) {
			t.Errorf("OAuth completion page contains simplified term %q", term)
		}
	}
}

func TestRootPageServesLocalizedTemplates(t *testing.T) {
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	s := &Server{}
	rr := httptest.NewRecorder()
	s.rootPage(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `lang="zh-TW"`) {
		t.Fatalf("localized login template not served: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCanonicalManagementIdentifiersRemainEnglish(t *testing.T) {
	expected := map[string][]string{
		"web/index.html": {"expiresAt", "updatedAt", "durationMs"},
		"web/debug.html": {"requestId", "expiresAt", "durationMs"},
	}
	for path, identifiers := range expected {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, identifier := range identifiers {
			if !strings.Contains(text, identifier) {
				t.Errorf("%s lost canonical identifier %q", path, identifier)
			}
		}
	}
}

func TestLocalizedAdministrationInlineJavaScriptIsValid(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	scriptPattern := regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)
	for _, path := range []string{"web/login.html", "web/index.html", "web/debug.html"} {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		matches := scriptPattern.FindAllSubmatch(raw, -1)
		var scripts strings.Builder
		for _, match := range matches {
			if len(match) == 2 && len(strings.TrimSpace(string(match[1]))) > 0 {
				scripts.Write(match[1])
				scripts.WriteByte('\n')
			}
		}
		jsPath := filepath.Join(t.TempDir(), filepath.Base(path)+".js")
		if err := os.WriteFile(jsPath, []byte(scripts.String()), 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(node, "--check", jsPath).CombinedOutput(); err != nil {
			t.Fatalf("%s localized JavaScript is invalid: %v\n%s", path, err, output)
		}
	}
}

func TestVisibleManagementHandlersReturnZhTWErrors(t *testing.T) {
	server := &Server{debug: openDebugStoreWithPolicy(filepath.Join(t.TempDir(), "debug.json"), defaultDebugStorePolicy())}
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		method      string
		target      string
		body        string
		wantStatus  int
		wantMessage string
		wantType    string
	}{
		{name: "admin login method", handler: server.adminLogin, method: http.MethodGet, target: "/api/admin/login", wantStatus: http.StatusMethodNotAllowed, wantMessage: "不支援此 HTTP 方法", wantType: "invalid_request_error"},
		{name: "change password method", handler: server.adminChangePassword, method: http.MethodGet, target: "/api/admin/change-password", wantStatus: http.StatusMethodNotAllowed, wantMessage: "不支援此 HTTP 方法", wantType: "invalid_request_error"},
		{name: "debug record missing", handler: server.debugDetail, method: http.MethodGet, target: "/api/admin/debug/detail?id=missing", wantStatus: http.StatusNotFound, wantMessage: "找不到診斷記錄", wantType: "not_found"},
		{name: "proxy malformed json", handler: server.proxyPool, method: http.MethodPost, target: "/api/admin/proxy-pool", body: "{", wantStatus: http.StatusBadRequest, wantMessage: "JSON 格式錯誤", wantType: "invalid_request_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body)))
			if rr.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var response struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
			}
			if response.Error.Message != tc.wantMessage || response.Error.Type != tc.wantType {
				t.Fatalf("error=%+v want message=%q type=%q", response.Error, tc.wantMessage, tc.wantType)
			}
		})
	}
}

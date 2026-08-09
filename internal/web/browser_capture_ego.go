package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	egoBrowserAttachStartupTimeout = 20 * time.Second
	egoBrowserCaptureResponseLimit = 16 << 10
)

type egoBrowserCaptureRequest struct {
	AuthorizationURL string `json:"authorizationUrl"`
	RedirectURI      string `json:"redirectUri"`
	State            string `json:"state"`
	TimeoutMS        int64  `json:"timeoutMs"`
}

type egoBrowserCaptureResponse struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

func runEgoBrowserPKCECapture(ctx context.Context, request browserPKCECaptureRequest, taskSpace string) (browserPKCECapturedAuthorization, error) {
	taskSpace = strings.TrimSpace(taskSpace)
	if taskSpace == "" {
		return browserPKCECapturedAuthorization{}, errors.New("ego-browser task space is required")
	}
	if err := validateBrowserAuthorizationURL(request.AuthorizationURL); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if err := validateBrowserRedirectURI(request.RedirectURI); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if strings.TrimSpace(request.State) == "" {
		return browserPKCECapturedAuthorization{}, errors.New("browser PKCE state is required")
	}

	egoPath, err := resolveEgoBrowserExecutable()
	if err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	listener, socketPath, err := listenEgoBrowserControlSocket()
	if err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	defer func() {
		_ = listener.Close()
		_ = os.RemoveAll(filepath.Dir(socketPath))
	}()

	script := egoBrowserCaptureScript(socketPath, taskSpace)
	cmd := exec.CommandContext(ctx, egoPath, "nodejs", "-e", script)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return browserPKCECapturedAuthorization{}, fmt.Errorf("start ego-browser capture adapter: %w", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()

	conn, err := acceptEgoBrowserControlConnection(ctx, listener)
	if err != nil {
		stopEgoBrowserCaptureProcess(cmd.Process, childDone)
		return browserPKCECapturedAuthorization{}, err
	}
	defer conn.Close()

	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		_ = conn.SetDeadline(deadline)
	}
	timeoutMS := int64(pkceTransactionTTL / time.Millisecond)
	if hasDeadline {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < pkceTransactionTTL {
			timeoutMS = int64(remaining / time.Millisecond)
		}
	}
	payload := egoBrowserCaptureRequest{
		AuthorizationURL: request.AuthorizationURL,
		RedirectURI:      request.RedirectURI,
		State:            request.State,
		TimeoutMS:        timeoutMS,
	}
	if err := json.NewEncoder(conn).Encode(payload); err != nil {
		stopEgoBrowserCaptureProcess(cmd.Process, childDone)
		return browserPKCECapturedAuthorization{}, errors.New("send ego-browser capture request")
	}

	reader := bufio.NewReader(io.LimitReader(conn, egoBrowserCaptureResponseLimit+1))
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		stopEgoBrowserCaptureProcess(cmd.Process, childDone)
		return browserPKCECapturedAuthorization{}, errors.New("receive ego-browser capture result")
	}
	if len(line) == 0 || len(line) > egoBrowserCaptureResponseLimit {
		stopEgoBrowserCaptureProcess(cmd.Process, childDone)
		return browserPKCECapturedAuthorization{}, errors.New("invalid ego-browser capture result")
	}
	var response egoBrowserCaptureResponse
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		stopEgoBrowserCaptureProcess(cmd.Process, childDone)
		return browserPKCECapturedAuthorization{}, errors.New("decode ego-browser capture result")
	}

	stopEgoBrowserCaptureProcess(cmd.Process, childDone)
	return browserPKCECapturedAuthorization{
		Code:  strings.TrimSpace(response.Code),
		State: strings.TrimSpace(response.State),
		Error: strings.TrimSpace(response.Error),
	}, nil
}

func resolveEgoBrowserExecutable() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("M365_EGO_BROWSER_PATH")); explicit != "" {
		if browserExecutableExists(explicit) {
			return explicit, nil
		}
		return "", errors.New("configured ego-browser executable is unavailable")
	}
	path, err := exec.LookPath("ego-browser")
	if err != nil {
		return "", errors.New("ego-browser executable was not found")
	}
	return path, nil
}

func listenEgoBrowserControlSocket() (net.Listener, string, error) {
	if runtime.GOOS == "windows" {
		return nil, "", errors.New("ego-browser attach capture requires a Unix control socket")
	}
	dir, err := os.MkdirTemp("", "m365-ego-")
	if err != nil {
		return nil, "", fmt.Errorf("create ego-browser control directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("secure ego-browser control directory: %w", err)
	}
	socketPath := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("listen for ego-browser capture adapter: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("secure ego-browser control socket: %w", err)
	}
	return listener, socketPath, nil
}

func acceptEgoBrowserControlConnection(ctx context.Context, listener net.Listener) (net.Conn, error) {
	acceptCtx, cancel := context.WithTimeout(ctx, egoBrowserAttachStartupTimeout)
	defer cancel()
	accepted := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- struct {
			conn net.Conn
			err  error
		}{conn: conn, err: err}
	}()
	select {
	case <-acceptCtx.Done():
		_ = listener.Close()
		return nil, fmt.Errorf("ego-browser capture adapter startup: %w", acceptCtx.Err())
	case result := <-accepted:
		if result.err != nil {
			return nil, fmt.Errorf("accept ego-browser capture adapter: %w", result.err)
		}
		return result.conn, nil
	}
}

func stopEgoBrowserCaptureProcess(process *os.Process, childDone <-chan error) {
	select {
	case <-childDone:
		return
	case <-time.After(2 * time.Second):
	}
	if process != nil {
		_ = process.Kill()
	}
	select {
	case <-childDone:
	case <-time.After(time.Second):
	}
}

func egoBrowserCaptureScript(socketPath, taskSpace string) string {
	replacer := strings.NewReplacer(
		"{{SOCKET_PATH}}", strconv.Quote(socketPath),
		"{{TASK_SPACE}}", strconv.Quote(taskSpace),
	)
	return replacer.Replace(egoBrowserCaptureScriptTemplate)
}

const egoBrowserCaptureScriptTemplate = `(async()=>{
const net = await import("node:net")
const socketPath = {{SOCKET_PATH}}
const taskSpace = {{TASK_SPACE}}
const socket = net.createConnection({path: socketPath})
socket.setEncoding("utf8")
let controlClosed = false
socket.on("close",()=>{ controlClosed = true })
const input = await new Promise((resolve,reject)=>{
  let raw = ""
  const fail = err => reject(err)
  socket.once("error", fail)
  socket.on("data", chunk => {
    raw += chunk
    const newline = raw.indexOf("\n")
    if (newline < 0) return
    socket.removeListener("error", fail)
    try { resolve(JSON.parse(raw.slice(0,newline))) } catch (err) { reject(err) }
  })
})
let authTarget = null
let preexistingTargets = new Set()
const finish = result => new Promise(resolve => socket.end(JSON.stringify(result)+"\n", resolve))
const closeAuthTab = async () => {
  if (!authTarget) return
  for (let attempt = 0; attempt < 5; attempt++) {
    const tabs = await listTabs()
    const present = tabs.some(tab => tab && tab.targetId === authTarget)
    if (!present) {
      authTarget = null
      return
    }
    const fallback = tabs.find(tab => tab && tab.targetId && preexistingTargets.has(tab.targetId))
    if (fallback) {
      try { await switchTab(fallback.targetId) } catch {}
    }
    try { await closeTab(authTarget) } catch {
      try { await cdp("Target.closeTarget",{targetId:authTarget}) } catch {}
    }
    await wait(0.1)
  }
  if ((await listTabs()).some(tab => tab && tab.targetId === authTarget)) throw new Error("ego-browser scratch tab cleanup failed")
  authTarget = null
}
const sameRedirect = (candidate, redirect) => {
  try {
    const a = new URL(candidate)
    const b = new URL(redirect)
    if (a.username || a.password || a.hash || b.username || b.password || b.hash || b.search) return false
    const pathA = a.pathname || "/"
    const pathB = b.pathname || "/"
    return a.protocol.toLowerCase() === b.protocol.toLowerCase() && a.host.toLowerCase() === b.host.toLowerCase() && pathA === pathB
  } catch { return false }
}
const capturedAuthorization = candidate => {
  if (!candidate || !sameRedirect(candidate,input.redirectUri)) return null
  try {
    const url = new URL(candidate)
    const states = url.searchParams.getAll("state")
    const codes = url.searchParams.getAll("code")
    const errors = url.searchParams.getAll("error")
    if (states.length !== 1 || states[0] !== input.state) return null
    if (codes.length > 1 || errors.length > 1 || (codes.length === 1) === (errors.length === 1)) return null
    return {code: codes[0] || "", state: states[0], error: errors[0] || ""}
  } catch { return null }
}
try {
  const spaces = await listTaskSpaces()
  const matches = spaces.filter(space => String(space && space.id) === String(taskSpace) || String(space && space.taskId) === String(taskSpace) || String(space && space.name) === String(taskSpace))
  if (matches.length !== 1) throw new Error("ego-browser task space selector is ambiguous or unavailable")
  const selected = matches[0]
  if (selected.ownership !== "agent" || selected.id === undefined || selected.id === null) throw new Error("ego-browser task space is not agent-controlled")
  await useOrCreateTaskSpace(selected.id)
  preexistingTargets = new Set((await listTabs()).map(tab => tab && tab.targetId).filter(Boolean))
  const opened = await createTab("about:blank")
  authTarget = opened && opened.targetId
  if (!authTarget) throw new Error("ego-browser scratch tab unavailable")
  await switchTab(authTarget)
  await cdp("Network.enable",{})
  await cdp("Page.enable",{})
  await drainEvents()
  await gotoUrl(input.authorizationUrl)
  const deadline = Date.now() + Math.max(1000,Math.min(Number(input.timeoutMs)||600000,600000))
  while (!controlClosed && Date.now() < deadline) {
    const events = await drainEvents()
    for (const event of events) {
      let candidate = ""
      if (event && event.method === "Network.requestWillBeSent") candidate = event.params && event.params.request && event.params.request.url || ""
      if (event && event.method === "Page.frameRequestedNavigation") candidate = event.params && event.params.url || ""
      const captured = capturedAuthorization(candidate)
      if (captured) {
        await closeAuthTab()
        await finish(captured)
        return
      }
    }
    await wait(0.2)
  }
  if (controlClosed) throw new Error("control channel closed")
  throw new Error("capture timeout")
} catch {
  socket.destroy()
  process.exitCode = 1
} finally {
  try { await closeAuthTab() } catch {}
}
})()`

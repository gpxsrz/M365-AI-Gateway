package web

import (
	"context"
	"fmt"
	"log"
	"m365-native/internal/chathub"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type checkpointRequestMode uint8

const (
	checkpointFullHistory checkpointRequestMode = iota
	checkpointResponseParent
	checkpointAppendOnly
)

type checkpointRequestControl struct {
	Mode       checkpointRequestMode
	Namespace  string
	Key        string
	ParentID   string
	ResponseID string
	ForceNew   bool
	Untracked  bool
}

type checkpointRequestContextKey struct{}
type checkpointExecutionContextKey struct{}

func transportCheckpointPath() string {
	if path := strings.TrimSpace(os.Getenv("M365_SESSION_CACHE")); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "m365-native", "transport-checkpoints.json")
}

func openConfiguredTransportCheckpointStore() (*transportCheckpointStore, error) {
	if strings.TrimSpace(os.Getenv("M365_SESSION_CACHE")) == "" {
		legacy := filepath.Join(os.TempDir(), "m365-native-sessions.json")
		if err := removeLegacyDefaultSessionCache(legacy); err != nil {
			return nil, err
		}
	}
	return openTransportCheckpointStore(transportCheckpointPath())
}

func removeLegacyDefaultSessionCache(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy session cache: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("legacy session cache path is a directory")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove legacy session cache: %w", err)
	}
	return nil
}

func (s *Server) resetTransportCheckpoints(change func() error) error {
	if s == nil {
		return nil
	}
	s.checkpointLifecycle.Lock()
	defer s.checkpointLifecycle.Unlock()
	if s.checkpoints != nil {
		return s.checkpoints.ClearThen(change)
	}
	if change != nil {
		return change()
	}
	return nil
}

func withCheckpointRequest(rctx context.Context, control checkpointRequestControl) context.Context {
	return context.WithValue(rctx, checkpointRequestContextKey{}, control)
}

func checkpointRequestFrom(rctx context.Context) checkpointRequestControl {
	control, _ := rctx.Value(checkpointRequestContextKey{}).(checkpointRequestControl)
	return control
}

type checkpointOutcome struct {
	binding  checkpointBinding
	produced []oaiMsg
}

type checkpointExecution struct {
	turn    *publicCheckpointTurn
	outcome checkpointOutcome
}

func withCheckpointExecution(ctx context.Context, execution *checkpointExecution) context.Context {
	return context.WithValue(ctx, checkpointExecutionContextKey{}, execution)
}

func checkpointExecutionFrom(ctx context.Context) *checkpointExecution {
	execution, _ := ctx.Value(checkpointExecutionContextKey{}).(*checkpointExecution)
	return execution
}

func (e *checkpointExecution) Capture(result chathub.Result, produced ...oaiMsg) {
	if e == nil || e.turn == nil {
		return
	}
	e.turn.Observe(result)
	e.outcome = checkpointOutcome{binding: e.turn.binding, produced: append([]oaiMsg(nil), produced...)}
}

func (e *checkpointExecution) Request(request chathub.Request) chathub.Request {
	if e == nil || e.turn == nil {
		return request
	}
	return e.turn.Request(request)
}

func (e *checkpointExecution) Observe(result chathub.Result) {
	if e != nil && e.turn != nil {
		e.turn.Observe(result)
	}
}

func (e *checkpointExecution) Accept() error {
	if e == nil || e.turn == nil || e.turn.turn == nil {
		return nil
	}
	if len(e.outcome.produced) == 0 {
		return fmt.Errorf("checkpoint acceptance requires a caller-visible assistant result")
	}
	e.turn.binding = e.outcome.binding
	return e.turn.Accept(e.outcome.produced...)
}

func (e *checkpointExecution) Abort() {
	if e != nil && e.turn != nil {
		e.turn.Abort()
	}
}

func completeCheckpointExecution(execution *checkpointExecution, owns bool, result chathub.Result, produced ...oaiMsg) error {
	if execution == nil {
		return nil
	}
	execution.Capture(result, produced...)
	if owns {
		return execution.Accept()
	}
	return nil
}

// publicCheckpointTurn is the request-scoped owner of a single checkpoint
// lease. Internal router/repair calls may rotate its upstream SessionID, but
// only the assistant message actually returned to the caller is accepted.
type publicCheckpointTurn struct {
	turn       *checkpointTurn
	binding    checkpointBinding
	responseID string
	observeErr error
	release    sync.Once
	unlock     func()
}

func (s *Server) beginOpenAICheckpoint(ctx context.Context, body *oaiReq, validators ...func([]oaiMsg) error) (*publicCheckpointTurn, error) {
	var validateOutbound func([]oaiMsg) error
	if len(validators) > 0 {
		validateOutbound = validators[0]
	}
	if s.checkpoints == nil {
		if validateOutbound != nil {
			if err := validateOutbound(body.Messages); err != nil {
				return nil, err
			}
		}
		return &publicCheckpointTurn{}, nil
	}
	control := checkpointRequestFrom(ctx)
	if control.Namespace == "" {
		control.Namespace = "chat-completions"
	}
	startedAt := time.Now()
	s.checkpointLifecycle.RLock()
	lifecycleWait := time.Since(startedAt)
	unlock := true
	defer func() {
		if unlock {
			s.checkpointLifecycle.RUnlock()
		}
	}()
	if control.Untracked {
		if validateOutbound != nil {
			if err := validateOutbound(body.Messages); err != nil {
				logCheckpointBegin(control.Namespace, lifecycleWait, time.Since(startedAt), err)
				return nil, err
			}
		}
		body.ConversationID = ""
		body.SessionID = ""
		body.SessionKey = ""
		logCheckpointBegin(control.Namespace, lifecycleWait, time.Since(startedAt), nil)
		return &publicCheckpointTurn{}, nil
	}
	owner := apiKeyOwnerFromContext(ctx)
	var (
		turn *checkpointTurn
		err  error
	)
	switch control.Mode {
	case checkpointResponseParent:
		if validateOutbound != nil {
			err = validateOutbound(body.Messages)
			if err != nil {
				break
			}
		}
		turn, err = s.checkpoints.BeginResponse(owner, control.ParentID, body.Messages)
	case checkpointAppendOnly:
		if validateOutbound != nil {
			err = validateOutbound(body.Messages)
			if err != nil {
				break
			}
		}
		turn, err = s.checkpoints.BeginDelta(control.Namespace, owner, control.Key, body.Messages)
	default:
		key := control.Key
		if key == "" {
			key = body.SessionKey
		}
		turn, err = s.checkpoints.BeginFullValidated(control.Namespace, owner, key, body.Messages, control.ForceNew, validateOutbound)
	}
	if err != nil {
		logCheckpointBegin(control.Namespace, lifecycleWait, time.Since(startedAt), err)
		return nil, err
	}
	body.Messages = append([]oaiMsg(nil), turn.Outbound...)
	body.ConversationID = turn.Binding.ConversationID
	body.SessionID = turn.Binding.SessionID
	logCheckpointBegin(control.Namespace, lifecycleWait, time.Since(startedAt), nil)
	unlock = false
	return &publicCheckpointTurn{turn: turn, binding: turn.Binding, responseID: control.ResponseID, unlock: s.checkpointLifecycle.RUnlock}, nil
}

func logCheckpointBegin(namespace string, lifecycleWait, total time.Duration, err error) {
	if lifecycleWait < 100*time.Millisecond && total < 100*time.Millisecond {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	log.Printf("[checkpoint-trace] operation=begin namespace=%s lifecycle_wait_ms=%d total_ms=%d result=%s", namespace, lifecycleWait.Milliseconds(), total.Milliseconds(), result)
}

func (s *Server) beginLegacyCheckpoint(key, text string) (*publicCheckpointTurn, error) {
	if s.checkpoints == nil || strings.TrimSpace(key) == "" {
		return &publicCheckpointTurn{}, nil
	}
	s.checkpointLifecycle.RLock()
	turn, err := s.checkpoints.BeginDelta("legacy", "administrator", key, []oaiMsg{{Role: "user", Content: text}})
	if err != nil {
		s.checkpointLifecycle.RUnlock()
		return nil, err
	}
	return &publicCheckpointTurn{turn: turn, binding: turn.Binding, unlock: s.checkpointLifecycle.RUnlock}, nil
}

func (t *publicCheckpointTurn) Request(request chathub.Request) chathub.Request {
	if t == nil || t.turn == nil {
		return request
	}
	request.ConversationID = t.binding.ConversationID
	request.SessionID = t.binding.SessionID
	return request
}

func (t *publicCheckpointTurn) Observe(result chathub.Result) {
	if t == nil || t.turn == nil {
		return
	}
	if conversationID := strings.TrimSpace(result.ConversationID); conversationID != "" {
		if t.binding.ConversationID != "" && t.binding.ConversationID != conversationID {
			t.observeErr = ErrCheckpointConversationDrift
			return
		}
		t.binding.ConversationID = conversationID
	}
	if strings.TrimSpace(result.SessionID) != "" {
		t.binding.SessionID = result.SessionID
	}
}

func (t *publicCheckpointTurn) Accept(produced ...oaiMsg) error {
	if t == nil || t.turn == nil {
		return nil
	}
	defer t.releaseLease()
	if t.observeErr != nil {
		_ = t.turn.Abort()
		return t.observeErr
	}
	if strings.TrimSpace(t.binding.ConversationID) == "" {
		return fmt.Errorf("checkpoint acceptance requires an upstream conversation ID")
	}
	return t.turn.Accept(t.binding, produced, t.responseID)
}

func (t *publicCheckpointTurn) Abort() {
	if t == nil {
		return
	}
	defer t.releaseLease()
	if t.turn != nil {
		_ = t.turn.Abort()
	}
}

func (t *publicCheckpointTurn) releaseLease() {
	if t != nil && t.unlock != nil {
		t.release.Do(t.unlock)
	}
}

func assistantTextCheckpointMessage(text string, images []string) oaiMsg {
	images = validImageURLs(images)
	content := any(text)
	if len(images) > 0 {
		parts := []any{map[string]any{"type": "text", "text": text}}
		for _, image := range images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}})
		}
		content = parts
	}
	return oaiMsg{Role: "assistant", Content: content}
}

func assistantToolCheckpointMessage(calls []detectedToolCall, result chathub.Result, stream bool) oaiMsg {
	content := ""
	if !stream && strings.TrimSpace(result.Text) != "" {
		content = result.Text
	}
	return assistantToolCheckpointMessageWithContent(calls, content, result.Images)
}

func assistantToolCheckpointMessageWithContent(calls []detectedToolCall, content string, images []string) oaiMsg {
	message := oaiMsg{Role: "assistant"}
	images = validImageURLs(images)
	if len(images) > 0 {
		parts := make([]any, 0, len(images)+1)
		if content != "" {
			parts = append(parts, map[string]any{"type": "text", "text": content})
		}
		for _, image := range images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}})
		}
		message.Content = parts
	} else if content != "" {
		message.Content = content
	}
	message.ToolCalls = checkpointToolCalls(calls)
	return message
}

func checkpointToolCalls(calls []detectedToolCall) []map[string]any {
	toolCalls := make([]map[string]any, 0, len(calls))
	for _, raw := range toolCallMaps(calls) {
		if call, ok := raw.(map[string]any); ok {
			toolCalls = append(toolCalls, call)
		}
	}
	return toolCalls
}

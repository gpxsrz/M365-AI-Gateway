package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultStdioMaxMessageBytes = 8 << 20
	defaultStdioMaxPending      = 64
	stdioCloseGrace             = 2 * time.Second
)

type StdioOptions struct {
	Environment     []string
	MaxMessageBytes int
	MaxPending      int
}

type StdioClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writes    chan stdioWrite
	mu        sync.Mutex
	pending   map[string]chan stdioReply
	contracts map[string]Tool
	nextID    int64
	maxBytes  int
	maxCalls  int
	done      chan struct{}
	waitDone  chan struct{}
	close     sync.Once
}

type stdioWrite struct {
	body   []byte
	result chan error
}

type stdioReply struct {
	raw json.RawMessage
	err error
}

type stdioResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func StartStdio(ctx context.Context, command string, args []string, options StdioOptions) (*StdioClient, error) {
	if command == "" {
		return nil, errors.New("stdio command required")
	}
	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultStdioMaxMessageBytes
	}
	if options.MaxPending <= 0 {
		options.MaxPending = defaultStdioMaxPending
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if options.Environment != nil {
		cmd.Env = append([]string(nil), options.Environment...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdio stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdio stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start stdio MCP server: %w", err)
	}
	client := &StdioClient{
		cmd:      cmd,
		stdin:    stdin,
		writes:   make(chan stdioWrite, options.MaxPending),
		pending:  make(map[string]chan stdioReply),
		maxBytes: options.MaxMessageBytes,
		maxCalls: options.MaxPending,
		done:     make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()
	go client.writeLoop()
	go client.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		close(client.waitDone)
	}()
	return client, nil
}

func (c *StdioClient) Initialize(ctx context.Context) error {
	response, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": LatestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "m365-copilot2api", "version": "wp6"},
	})
	if err != nil {
		return err
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if decodeExactJSON(response.Result, &result) != nil {
		return errors.New("invalid MCP initialize result")
	}
	if _, supported := supportedProtocolVersions[result.ProtocolVersion]; !supported {
		return fmt.Errorf("unsupported MCP protocol version %q", result.ProtocolVersion)
	}
	return c.notification(ctx, "notifications/initialized", map[string]any{})
}

func (c *StdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	response, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []Tool `json:"tools"`
	}
	if decodeExactJSON(response.Result, &result) != nil {
		return nil, errors.New("invalid MCP tools/list result")
	}
	tools, err := normalizeTools(result.Tools)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return nil, errors.New("invalid MCP tools/list result")
	}
	var cached []Tool
	if decodeExactJSON(encoded, &cached) != nil {
		return nil, errors.New("invalid MCP tools/list result")
	}
	contracts := make(map[string]Tool, len(cached))
	for _, tool := range cached {
		contracts[tool.Name] = tool
	}
	c.mu.Lock()
	c.contracts = contracts
	c.mu.Unlock()
	return tools, nil
}

func (c *StdioClient) CallTool(ctx context.Context, name string, arguments map[string]any) (CallResult, error) {
	if !validToolName(name) {
		return CallResult{}, errors.New("invalid MCP tool name")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	c.mu.Lock()
	tool, found := c.contracts[name]
	listed := c.contracts != nil
	c.mu.Unlock()
	if listed && !found {
		return CallResult{}, errors.New("unknown MCP tool")
	}
	if listed {
		if err := validateToolValue(tool.InputSchema, arguments); err != nil {
			return CallResult{}, errors.New("MCP tool arguments do not match input schema")
		}
	}
	response, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return CallResult{}, err
	}
	var result CallResult
	if decodeExactJSON(response.Result, &result) != nil {
		return CallResult{}, errors.New("invalid MCP tools/call result")
	}
	result, err = normalizeCallResult(tool, result)
	if err != nil {
		return CallResult{}, errors.New("invalid MCP tools/call result")
	}
	return result, nil
}

func (c *StdioClient) Close() error {
	c.close.Do(func() {
		_ = c.stdin.Close()
		c.failPending(errors.New("MCP stdio client closed"))
		select {
		case <-c.waitDone:
		case <-time.After(stdioCloseGrace):
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			<-c.waitDone
		}
	})
	return nil
}

func (c *StdioClient) PendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func (c *StdioClient) request(ctx context.Context, method string, params any) (*stdioResponse, error) {
	c.mu.Lock()
	if len(c.pending) >= c.maxCalls {
		c.mu.Unlock()
		return nil, errors.New("too many pending MCP stdio calls")
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, errors.New("MCP stdio server exited")
	default:
	}
	c.nextID++
	id := c.nextID
	key := fmt.Sprint(id)
	reply := make(chan stdioReply, 1)
	c.pending[key] = reply
	c.mu.Unlock()

	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := c.writeJSON(ctx, request); err != nil {
		c.removePending(key)
		if ctx.Err() != nil {
			c.cancelRequest(id)
		}
		return nil, err
	}

	select {
	case received := <-reply:
		if received.err != nil {
			return nil, received.err
		}
		var response stdioResponse
		if json.Unmarshal(received.raw, &response) != nil || response.JSONRPC != "2.0" {
			return nil, errors.New("invalid MCP stdio response")
		}
		if response.Error != nil {
			return nil, fmt.Errorf("MCP RPC error %d: %s", response.Error.Code, response.Error.Message)
		}
		return &response, nil
	case <-ctx.Done():
		c.removePending(key)
		c.cancelRequest(id)
		return nil, ctx.Err()
	case <-c.done:
		c.removePending(key)
		return nil, errors.New("MCP stdio server exited")
	}
}

func (c *StdioClient) cancelRequest(id int64) {
	c.tryWriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/cancelled",
		"params":  map[string]any{"requestId": id, "reason": "client request cancelled"},
	})
}

func (c *StdioClient) notification(ctx context.Context, method string, params any) error {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	return c.writeJSON(ctx, message)
}

func (c *StdioClient) writeJSON(ctx context.Context, message any) error {
	body, err := c.marshalJSON(message)
	if err != nil {
		return err
	}
	write := stdioWrite{body: body, result: make(chan error, 1)}
	select {
	case c.writes <- write:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("MCP stdio server exited")
	}
	select {
	case err := <-write.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("MCP stdio server exited")
	}
}

func (c *StdioClient) tryWriteJSON(message any) {
	body, err := c.marshalJSON(message)
	if err != nil {
		return
	}
	write := stdioWrite{body: body, result: make(chan error, 1)}
	select {
	case c.writes <- write:
	case <-c.done:
	default:
	}
}

func (c *StdioClient) marshalJSON(message any) ([]byte, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if len(body) > c.maxBytes {
		return nil, errors.New("MCP stdio message too large")
	}
	return append(body, '\n'), nil
}

func (c *StdioClient) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case write := <-c.writes:
			_, err := c.stdin.Write(write.body)
			if err != nil {
				write.result <- errors.New("write MCP stdio message failed")
				c.failPending(errors.New("MCP stdio stream closed"))
				return
			}
			write.result <- nil
		}
	}
}

func (c *StdioClient) readLoop(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readBoundedLine(reader, c.maxBytes)
		if err != nil {
			c.failPending(errors.New("MCP stdio stream closed"))
			return
		}
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if json.Unmarshal(line, &envelope) != nil || envelope.JSONRPC != "2.0" || !validRequestID(envelope.ID) {
			c.failPending(errors.New("invalid MCP stdio response"))
			return
		}
		if envelope.Method != "" {
			if len(envelope.ID) > 0 {
				c.tryWriteJSON(newRPCError(envelope.ID, -32601, "method not found"))
			}
			continue
		}
		if len(envelope.ID) == 0 {
			c.failPending(errors.New("invalid MCP stdio response"))
			return
		}
		key := string(envelope.ID)
		c.mu.Lock()
		reply := c.pending[key]
		delete(c.pending, key)
		c.mu.Unlock()
		if reply != nil {
			reply <- stdioReply{raw: append(json.RawMessage(nil), line...)}
		}
	}
}

func readBoundedLine(reader *bufio.Reader, max int) ([]byte, error) {
	line := make([]byte, 0, min(max, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > max+1 {
			return nil, errors.New("MCP stdio message too large")
		}
		line = append(line, fragment...)
		if err == nil {
			return line[:len(line)-1], nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (c *StdioClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *StdioClient) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan stdioReply)
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
		close(c.done)
	}
	c.mu.Unlock()
	for _, reply := range pending {
		reply <- stdioReply{err: err}
	}
}

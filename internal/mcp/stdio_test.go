package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("WP6_MCP_STDIO_HELPER") != "1" {
		return
	}
	if os.Getenv("WP6_MCP_STDIO_NO_READ") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	_, _ = os.Stderr.WriteString(strings.Repeat("stderr-private-marker\n", 16384))
	reader := bufio.NewReaderSize(os.Stdin, 256*1024)
	writer := bufio.NewWriterSize(os.Stdout, 256*1024)
	initialized := false
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &request) != nil {
			continue
		}
		if request.Method == "notifications/initialized" {
			initialized = true
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": LatestProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			if !initialized {
				return
			}
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "large_echo",
				"description": strings.Repeat("S", 128*1024),
				"inputSchema": map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"marker": map[string]any{"type": "string"}},
					"required":             []any{"marker"},
					"additionalProperties": false,
				},
				"outputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"value": map[string]any{"type": []any{"string", "integer"}}},
					"required":   []any{"value"},
				},
			}, map[string]any{
				"name": "numeric_contract",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"value": map[string]any{"type": "integer", "const": json.Number("9007199254740993")}},
					"required":   []any{"value"},
				},
				"outputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"value": map[string]any{"type": "integer", "const": json.Number("9007199254740993")}},
					"required":   []any{"value"},
				},
			}}}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Name == "numeric_contract" {
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": "9007199254740993"}},
					"structuredContent": map[string]any{"value": json.Number("9007199254740993")},
				}
				break
			}
			if params.Arguments["marker"] == "NO_RESPONSE" {
				continue
			}
			marker := fmt.Sprint(params.Arguments["marker"])
			switch marker {
			case "BAD_OUTPUT":
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": "invalid output"}},
					"structuredContent": map[string]any{"value": false},
				}
			case "BAD_CONTENT":
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": 7}},
					"structuredContent": map[string]any{"value": marker},
				}
			case "BIG_INTEGER":
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": marker}},
					"structuredContent": map[string]any{"value": json.Number("9007199254740993")},
				}
			default:
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": marker}},
					"structuredContent": map[string]any{"value": marker},
				}
			}
		default:
			continue
		}
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(request.ID), "result": result})
		_, _ = writer.Write(append(response, '\n'))
		_ = writer.Flush()
	}
}

func TestWP6StdioListedToolContractIsDeepSafeAndValidatesCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartStdio(ctx, os.Args[0], []string{"-test.run=TestMCPStdioHelperProcess"}, StdioOptions{
		Environment: append(os.Environ(), "WP6_MCP_STDIO_HELPER=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	properties := tools[0].InputSchema["properties"].(map[string]any)
	properties["marker"].(map[string]any)["type"] = "number"

	if _, err := client.CallTool(ctx, "large_echo", map[string]any{"marker": 7}); err == nil {
		t.Fatal("caller mutation changed cached input contract")
	}
	if _, err := client.CallTool(ctx, "large_echo", map[string]any{}); err == nil {
		t.Fatal("schema-invalid arguments were sent")
	}
	if _, err := client.CallTool(ctx, "unlisted_but_valid", map[string]any{"marker": "value"}); err == nil {
		t.Fatal("unknown tool was sent after tools/list")
	}
	result, err := client.CallTool(ctx, "large_echo", map[string]any{"marker": "valid"})
	if err != nil || result.Text() != "valid" {
		t.Fatalf("valid listed tool call result=%q error=%v", result.Text(), err)
	}
	exact, err := client.CallTool(ctx, "large_echo", map[string]any{"marker": "BIG_INTEGER"})
	value, ok := exact.StructuredData["value"].(json.Number)
	if err != nil || !ok || value.String() != "9007199254740993" {
		t.Fatalf("large structured result=%#v error=%v", exact.StructuredData["value"], err)
	}
	contract, err := client.CallTool(ctx, "numeric_contract", map[string]any{"value": json.Number("9007199254740993")})
	contractValue, ok := contract.StructuredData["value"].(json.Number)
	if err != nil || !ok || contractValue.String() != "9007199254740993" {
		t.Fatalf("large numeric schema contract=%#v error=%v", contract.StructuredData["value"], err)
	}
}

func TestWP6StdioListedToolContractValidatesResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartStdio(ctx, os.Args[0], []string{"-test.run=TestMCPStdioHelperProcess"}, StdioOptions{
		Environment: append(os.Environ(), "WP6_MCP_STDIO_HELPER=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	properties := tools[0].OutputSchema["properties"].(map[string]any)
	properties["value"].(map[string]any)["type"] = "boolean"

	if _, err := client.CallTool(ctx, "large_echo", map[string]any{"marker": "BAD_OUTPUT"}); err == nil {
		t.Fatal("caller mutation changed cached output contract")
	}
	if _, err := client.CallTool(ctx, "large_echo", map[string]any{"marker": "BAD_CONTENT"}); err == nil {
		t.Fatal("malformed MCP content block was accepted")
	}
}

func TestWP6StdioCallWithoutListStillValidatesNameAndContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartStdio(ctx, os.Args[0], []string{"-test.run=TestMCPStdioHelperProcess"}, StdioOptions{
		Environment: append(os.Environ(), "WP6_MCP_STDIO_HELPER=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}

	result, err := client.CallTool(ctx, "valid_without_list", map[string]any{"marker": "valid"})
	if err != nil || result.Text() != "valid" {
		t.Fatalf("valid pre-list call result=%q error=%v", result.Text(), err)
	}
	if _, err := client.CallTool(ctx, "invalid name", map[string]any{"marker": "valid"}); err == nil {
		t.Fatal("invalid pre-list tool name was sent")
	}
	if _, err := client.CallTool(ctx, "valid_without_list", map[string]any{"marker": "BAD_CONTENT"}); err == nil {
		t.Fatal("malformed pre-list MCP content block was accepted")
	}
}

func TestWP6StdioWriteHonorsContextWhenServerDoesNotRead(t *testing.T) {
	processContext, cancelProcess := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProcess()
	client, err := StartStdio(processContext, os.Args[0], []string{"-test.run=TestMCPStdioHelperProcess"}, StdioOptions{
		Environment:     append(os.Environ(), "WP6_MCP_STDIO_HELPER=1", "WP6_MCP_STDIO_NO_READ=1"),
		MaxMessageBytes: 3 << 20,
		MaxPending:      2,
	})
	if err != nil {
		t.Fatal(err)
	}

	callContext, cancelCall := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelCall()
	callResult := make(chan error, 1)
	go func() {
		_, callErr := client.CallTool(callContext, "blocked", map[string]any{"payload": strings.Repeat("X", 2<<20)})
		callResult <- callErr
	}()

	returnedPromptly := false
	select {
	case callErr := <-callResult:
		returnedPromptly = true
		if !errors.Is(callErr, context.DeadlineExceeded) {
			t.Errorf("blocked stdio write error=%v", callErr)
		}
	case <-time.After(500 * time.Millisecond):
	}
	if returnedPromptly && client.PendingCount() != 0 {
		t.Errorf("blocked stdio write leaked %d pending call(s)", client.PendingCount())
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- client.Close() }()
	select {
	case closeErr := <-closeResult:
		if closeErr != nil {
			t.Errorf("close blocked stdio client: %v", closeErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after closing blocked stdio writer")
	}
	if !returnedPromptly {
		<-callResult
		t.Fatal("CallTool did not honor context deadline while child stdin was blocked")
	}
}

func TestWP6StdioInitializeLargeMessageConcurrentCorrelationAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := StartStdio(ctx, os.Args[0], []string{"-test.run=TestMCPStdioHelperProcess"}, StdioOptions{
		Environment:     append(os.Environ(), "WP6_MCP_STDIO_HELPER=1"),
		MaxMessageBytes: 512 * 1024,
		MaxPending:      8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || len(tools[0].Description) != 128*1024 {
		t.Fatalf("large stdio tool schema truncated: tools=%d description=%d", len(tools), len(tools[0].Description))
	}

	markers := []string{"A", "B", "C", "D"}
	errorResults := make(chan error, len(markers))
	var wg sync.WaitGroup
	for _, marker := range markers {
		marker := marker
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.CallTool(ctx, "large_echo", map[string]any{"marker": marker})
			if err == nil && result.Text() != marker {
				err = fmt.Errorf("marker %s correlated to %q", marker, result.Text())
			}
			errorResults <- err
		}()
	}
	wg.Wait()
	close(errorResults)
	for err := range errorResults {
		if err != nil {
			t.Fatal(err)
		}
	}
	cancelContext, cancelCall := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelCall()
	if _, err := client.CallTool(cancelContext, "large_echo", map[string]any{"marker": "NO_RESPONSE"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stdio cancellation error=%v", err)
	}
	if client.PendingCount() != 0 {
		t.Fatalf("stdio cancellation leaked %d pending call(s)", client.PendingCount())
	}

	var closeWG sync.WaitGroup
	closeErrors := make(chan error, 2)
	for range 2 {
		closeWG.Add(1)
		go func() { defer closeWG.Done(); closeErrors <- client.Close() }()
	}
	closeWG.Wait()
	close(closeErrors)
	for err := range closeErrors {
		if err != nil {
			t.Fatalf("idempotent close: %v", err)
		}
	}
}

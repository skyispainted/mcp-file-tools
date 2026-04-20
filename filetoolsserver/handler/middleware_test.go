package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testInput struct {
	Value string `json:"value"`
}

type testOutput struct {
	Result string `json:"result"`
}

func TestWithRecovery_NoPanic(t *testing.T) {
	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "success"}},
		}, testOutput{Result: "ok"}, nil
	}

	wrapped := WithRecovery(handler)
	result, output, err := wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{Value: "test"})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result.IsError {
		t.Error("expected non-error result")
	}
	if output.Result != "ok" {
		t.Errorf("expected output 'ok', got %q", output.Result)
	}
}

func TestWithRecovery_Panic(t *testing.T) {
	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		panic("test panic")
	}

	wrapped := WithRecovery(handler)
	result, _, err := wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{Value: "test"})

	if err != nil {
		t.Errorf("expected no error (panic handled via result), got %v", err)
	}
	if result == nil || !result.IsError {
		t.Error("expected error result")
	}
}

func TestWithRecovery_PanicWithNilValue(t *testing.T) {
	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		panic(nil)
	}

	wrapped := WithRecovery(handler)
	result, _, err := wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{Value: "test"})

	if err != nil {
		t.Errorf("expected no error (panic handled via result), got %v", err)
	}
	if result == nil || !result.IsError {
		t.Error("expected error result")
	}
}

func TestWithLogging_Success(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		return &mcp.CallToolResult{}, testOutput{Result: "ok"}, nil
	}

	wrapped := WithLogging(logger, "test_tool", handler)
	_, _, _ = wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "tool_call_start") {
		t.Error("expected tool_call_start log")
	}
	if !strings.Contains(logOutput, "tool_call_success") {
		t.Error("expected tool_call_success log")
	}
	if !strings.Contains(logOutput, "test_tool") {
		t.Error("expected tool name in log")
	}
}

func TestWithLogging_ToolError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "something went wrong"}},
			IsError: true,
		}, testOutput{}, nil
	}

	wrapped := WithLogging(logger, "test_tool", handler)
	_, _, _ = wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "tool_call_failed") {
		t.Error("expected tool_call_failed log")
	}
	if !strings.Contains(logOutput, "something went wrong") {
		t.Error("expected error message in log")
	}
}

func TestWithLogging_NilLogger(t *testing.T) {
	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		return &mcp.CallToolResult{}, testOutput{Result: "ok"}, nil
	}

	// Should not panic with nil logger
	wrapped := WithLogging(nil, "test_tool", handler)
	result, output, err := wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if output.Result != "ok" {
		t.Errorf("expected output 'ok', got %q", output.Result)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
}

func TestWrap_CombinesMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := func(ctx context.Context, req *mcp.CallToolRequest, input testInput) (*mcp.CallToolResult, testOutput, error) {
		panic("test panic in wrapped handler")
	}

	wrapped := Wrap(logger, "test_tool", handler)
	result, _, err := wrapped(context.Background(), &mcp.CallToolRequest{}, testInput{})

	// Should recover from panic
	if err != nil {
		t.Errorf("expected no error (panic handled via result), got %v", err)
	}
	if result == nil || !result.IsError {
		t.Error("expected error result")
	}

	// Logging middleware sees IsError result, logs as warning
	logOutput := buf.String()
	if !strings.Contains(logOutput, "tool_call_start") {
		t.Error("expected tool_call_start log")
	}
	if !strings.Contains(logOutput, "tool_call_failed") {
		t.Error("expected tool_call_failed log")
	}
}

func TestPreprocessEditArgs_NormalArray(t *testing.T) {
	raw := json.RawMessage(`{"path":"/test/file.txt","edits":[{"oldText":"old","newText":"new"}]}`)
	input, err := preprocessEditArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Path != "/test/file.txt" {
		t.Errorf("expected path '/test/file.txt', got %q", input.Path)
	}
	if len(input.Edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(input.Edits))
	}
	if input.Edits[0].OldText != "old" || input.Edits[0].NewText != "new" {
		t.Errorf("unexpected edit: %+v", input.Edits[0])
	}
}

func TestPreprocessEditArgs_StringifiedArray(t *testing.T) {
	raw := json.RawMessage(`{"path":"/test/file.txt","edits":"[{\"oldText\":\"old\",\"newText\":\"new\"}]"}`)
	input, err := preprocessEditArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Path != "/test/file.txt" {
		t.Errorf("expected path '/test/file.txt', got %q", input.Path)
	}
	if len(input.Edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(input.Edits))
	}
	if input.Edits[0].OldText != "old" || input.Edits[0].NewText != "new" {
		t.Errorf("unexpected edit: %+v", input.Edits[0])
	}
}

func TestPreprocessEditArgs_MultipleEdits(t *testing.T) {
	edits := `[{"oldText":"#include \"A.h\"\n#include \"B.h\"","newText":"#include \"A.h\"\n#include \"C.h\"\n#include \"B.h\""},{"oldText":"func1()","newText":"func2()"}]`
	raw := json.RawMessage(fmt.Sprintf(`{"path":"/test.cpp","edits":%s}`, edits))
	input, err := preprocessEditArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(input.Edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(input.Edits))
	}
}

func TestPreprocessEditArgs_StringifiedMultipleEdits(t *testing.T) {
	editsStr := `"[{\"oldText\":\"old1\",\"newText\":\"new1\"},{\"oldText\":\"old2\",\"newText\":\"new2\"}]"`
	raw := json.RawMessage(fmt.Sprintf(`{"path":"/test.cpp","edits":%s}`, editsStr))
	input, err := preprocessEditArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(input.Edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(input.Edits))
	}
	if input.Edits[0].OldText != "old1" || input.Edits[1].NewText != "new2" {
		t.Errorf("unexpected edits: %+v", input.Edits)
	}
}

func TestPreprocessEditArgs_DryRunAndEncoding(t *testing.T) {
	raw := json.RawMessage(`{"path":"/test.txt","edits":[],"dryRun":true,"encoding":"gbk"}`)
	input, err := preprocessEditArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !input.DryRun {
		t.Error("expected dryRun to be true")
	}
	if input.Encoding != "gbk" {
		t.Errorf("expected encoding 'gbk', got %q", input.Encoding)
	}
}

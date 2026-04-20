package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WithRecovery wraps a tool handler with panic recovery.
// If a panic occurs, it returns an error result instead of crashing the server.
func WithRecovery[In, Out any](handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, args In) (result *mcp.CallToolResult, output Out, err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered in tool handler", "panic", r, "stack", string(debug.Stack()))
				result = errorResult(fmt.Sprintf("internal error: panic in tool handler: %v", r))
			}
		}()
		return handler(ctx, req, args)
	}
}

// WithLogging wraps a tool handler with request/response logging.
// Logs tool name, duration, and any errors.
func WithLogging[In, Out any](logger *slog.Logger, toolName string, handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	if logger == nil {
		return handler
	}
	return func(ctx context.Context, req *mcp.CallToolRequest, args In) (*mcp.CallToolResult, Out, error) {
		logger.Debug("tool_call_start", "tool", toolName)

		result, output, err := handler(ctx, req, args)

		if err != nil {
			logger.Error("tool_call_error", "tool", toolName, "error", err)
		} else if result != nil && result.IsError {
			// Extract error message from result content
			var errMsg string
			if len(result.Content) > 0 {
				if tc, ok := result.Content[0].(*mcp.TextContent); ok {
					errMsg = tc.Text
				}
			}
			logger.Warn("tool_call_failed", "tool", toolName, "message", errMsg)
		} else {
			logger.Debug("tool_call_success", "tool", toolName)
		}

		return result, output, err
	}
}

// Wrap applies recovery and optional logging to a tool handler.
// This is the main entry point for wrapping handlers.
func Wrap[In, Out any](logger *slog.Logger, toolName string, handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	// Apply recovery first (outermost), then logging
	wrapped := WithRecovery(handler)
	if logger != nil {
		wrapped = WithLogging(logger, toolName, wrapped)
	}
	return wrapped
}

// WrapContentOnly wraps a handler so the SDK skips StructuredContent,
// returning only the handler's Content text (e.g. a readable diff).
func WrapContentOnly[In, Out any](logger *slog.Logger, toolName string, handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, any] {
	wrapped := Wrap(logger, toolName, handler)
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		result, _, err := wrapped(ctx, req, input)
		return result, nil, err
	}
}

// WrapEditFile registers edit_file with a raw ToolHandler that preprocesses
// arguments to handle the case where the MCP client sends the "edits" array
// as a JSON-encoded string instead of a proper JSON array.
func WrapEditFile(logger *slog.Logger, toolName string, h *Handler) mcp.ToolHandler {
	raw := WithRecoveryRaw(logger, toolName, h.HandleEditFileRaw)
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return raw(ctx, req)
	}
}

// preprocessEditArgs handles the case where "edits" is sent as a JSON string.
func preprocessEditArgs(raw json.RawMessage) (*EditFileInput, error) {
	// First, unmarshal into a generic map to inspect the edits field.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing arguments: %w", err)
	}

	var input EditFileInput

	// Extract simple fields.
	if p, ok := m["path"]; ok {
		if err := json.Unmarshal(p, &input.Path); err != nil {
			return nil, fmt.Errorf("parsing path: %w", err)
		}
	}
	if p, ok := m["dryRun"]; ok {
		json.Unmarshal(p, &input.DryRun) // best-effort
	}
	if p, ok := m["encoding"]; ok {
		json.Unmarshal(p, &input.Encoding) // best-effort
	}
	if p, ok := m["forceWritable"]; ok {
		var b bool
		if err := json.Unmarshal(p, &b); err == nil {
			input.ForceWritable = &b
		}
	}

	// Handle edits: could be a proper array or a JSON-encoded string.
	if editsRaw, ok := m["edits"]; ok && len(editsRaw) > 0 {
		// Try direct unmarshal first (normal case).
		if err := json.Unmarshal(editsRaw, &input.Edits); err == nil {
			return &input, nil
		}

		// Fallback: treat as a JSON string containing an array.
		var s string
		if err := json.Unmarshal(editsRaw, &s); err != nil {
			return nil, fmt.Errorf("parsing edits: expected array or JSON string, got %s", string(editsRaw))
		}
		if err := json.Unmarshal([]byte(s), &input.Edits); err != nil {
			return nil, fmt.Errorf("parsing edits from string: %w", err)
		}
	}

	return &input, nil
}

// HandleEditFileRaw is the raw handler for edit_file that preprocesses arguments.
func (h *Handler) HandleEditFileRaw(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := preprocessEditArgs(req.Params.Arguments)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	result, _, err := h.HandleEditFile(ctx, req, *input)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// WrapGrep registers grep_text_files with a raw ToolHandler that preprocesses
// arguments to handle the case where the MCP client sends the "paths" array
// as a JSON-encoded string instead of a proper JSON array.
func WrapGrep(logger *slog.Logger, toolName string, h *Handler) mcp.ToolHandler {
	raw := WithRecoveryRaw(logger, toolName, h.HandleGrepRaw)
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return raw(ctx, req)
	}
}

// preprocessGrepArgs handles the case where array fields are sent as JSON strings.
func preprocessGrepArgs(raw json.RawMessage) (*GrepInput, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing arguments: %w", err)
	}

	var input GrepInput

	// Extract simple fields.
	if p, ok := m["pattern"]; ok {
		if err := json.Unmarshal(p, &input.Pattern); err != nil {
			return nil, fmt.Errorf("parsing pattern: %w", err)
		}
	}
	if p, ok := m["include"]; ok {
		json.Unmarshal(p, &input.Include)
	}
	if p, ok := m["exclude"]; ok {
		json.Unmarshal(p, &input.Exclude)
	}
	if p, ok := m["encoding"]; ok {
		json.Unmarshal(p, &input.Encoding)
	}
	if p, ok := m["contextBefore"]; ok {
		json.Unmarshal(p, &input.ContextBefore)
	}
	if p, ok := m["contextAfter"]; ok {
		json.Unmarshal(p, &input.ContextAfter)
	}
	if p, ok := m["maxMatches"]; ok {
		json.Unmarshal(p, &input.MaxMatches)
	}
	if p, ok := m["caseSensitive"]; ok {
		var b bool
		if err := json.Unmarshal(p, &b); err == nil {
			input.CaseSensitive = &b
		}
	}

	// Handle paths: could be a proper array or a JSON-encoded string.
	if pathsRaw, ok := m["paths"]; ok && len(pathsRaw) > 0 {
		if err := json.Unmarshal(pathsRaw, &input.Paths); err != nil {
			var s string
			if err2 := json.Unmarshal(pathsRaw, &s); err2 == nil {
				if err3 := json.Unmarshal([]byte(s), &input.Paths); err3 != nil {
					return nil, fmt.Errorf("parsing paths from string: %w", err3)
				}
			} else {
				return nil, fmt.Errorf("parsing paths: expected array or JSON string, got %s", string(pathsRaw))
			}
		}
	}

	return &input, nil
}

// HandleGrepRaw is the raw handler for grep_text_files that preprocesses arguments.
func (h *Handler) HandleGrepRaw(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := preprocessGrepArgs(req.Params.Arguments)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	result, _, err := h.HandleGrep(ctx, req, *input)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// WrapReadMultipleFiles registers read_multiple_files with a raw ToolHandler that
// preprocesses arguments to handle stringified array parameters.
func WrapReadMultipleFiles(logger *slog.Logger, toolName string, h *Handler) mcp.ToolHandler {
	raw := WithRecoveryRaw(logger, toolName, h.HandleReadMultipleFilesRaw)
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return raw(ctx, req)
	}
}

// preprocessReadMultipleArgs handles stringified array parameters.
func preprocessReadMultipleArgs(raw json.RawMessage) (*ReadMultipleFilesInput, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing arguments: %w", err)
	}

	var input ReadMultipleFilesInput

	if p, ok := m["encoding"]; ok {
		json.Unmarshal(p, &input.Encoding)
	}

	// Handle paths: could be a proper array or a JSON-encoded string.
	if pathsRaw, ok := m["paths"]; ok && len(pathsRaw) > 0 {
		if err := json.Unmarshal(pathsRaw, &input.Paths); err != nil {
			var s string
			if err2 := json.Unmarshal(pathsRaw, &s); err2 == nil {
				if err3 := json.Unmarshal([]byte(s), &input.Paths); err3 != nil {
					return nil, fmt.Errorf("parsing paths from string: %w", err3)
				}
			} else {
				return nil, fmt.Errorf("parsing paths: expected array or JSON string, got %s", string(pathsRaw))
			}
		}
	}

	return &input, nil
}

// HandleReadMultipleFilesRaw is the raw handler for read_multiple_files that preprocesses arguments.
func (h *Handler) HandleReadMultipleFilesRaw(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, err := preprocessReadMultipleArgs(req.Params.Arguments)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	result, _, err := h.HandleReadMultipleFiles(ctx, req, *input)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// WithRecoveryRaw wraps a raw ToolHandler with panic recovery and optional logging.
func WithRecoveryRaw(logger *slog.Logger, toolName string, handler mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered in tool handler", "panic", r, "stack", string(debug.Stack()))
				result = errorResult(fmt.Sprintf("internal error: panic in tool handler: %v", r))
			}
		}()

		if logger != nil {
			logger.Debug("tool_call_start", "tool", toolName)
		}

		result, err = handler(ctx, req)

		if err != nil {
			if logger != nil {
				logger.Error("tool_call_error", "tool", toolName, "error", err)
			}
		} else if result != nil && result.IsError {
			var errMsg string
			if len(result.Content) > 0 {
				if tc, ok := result.Content[0].(*mcp.TextContent); ok {
					errMsg = tc.Text
				}
			}
			if logger != nil {
				logger.Warn("tool_call_failed", "tool", toolName, "message", errMsg)
			}
		} else {
			if logger != nil {
				logger.Debug("tool_call_success", "tool", toolName)
			}
		}

		return result, err
	}
}

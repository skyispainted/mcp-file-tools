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

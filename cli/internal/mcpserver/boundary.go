package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK's typed tool adapter validates and then re-encodes JSON through float64.
// Validate a disposable copy instead, keeping arguments and results exact on the wire.
func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	inputSchema, outputSchema := schemaFor[In](), schemaFor[Out]()
	tool.InputSchema, tool.OutputSchema = inputSchema, outputSchema
	resolved, err := inputSchema.Resolve(nil)
	if err != nil {
		panic(err)
	}
	server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fail := func(err error) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		raw := req.Params.Arguments
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			raw = json.RawMessage(`{}`)
		}
		var validation any
		if err := json.Unmarshal(raw, &validation); err != nil {
			return fail(fmt.Errorf("invalid arguments: %w", err))
		}
		if err := resolved.Validate(&validation); err != nil {
			return fail(fmt.Errorf("invalid arguments: %w", err))
		}
		var in In
		if err := json.Unmarshal(raw, &in); err != nil {
			return fail(fmt.Errorf("invalid arguments: %w", err))
		}
		result, out, err := handler(ctx, req, in)
		if err != nil {
			return fail(err)
		}
		if result == nil {
			result = &mcp.CallToolResult{}
		}
		if result.InputRequests != nil {
			return result, nil
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("encoding result: %w", err)
		}
		result.StructuredContent = json.RawMessage(encoded)
		if result.Content == nil {
			result.Content = []mcp.Content{&mcp.TextContent{Text: string(encoded)}}
		}
		return result, nil
	})
}

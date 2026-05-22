package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestHermaiMCPToolsExposeExpectedSurface(t *testing.T) {
	tools := hermaiMCPTools()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("tool %s has non-object input schema: %#v", tool.Name, tool.InputSchema)
		}
	}

	for _, name := range []string{
		"lookup_schema",
		"list_public_schemas",
		"submit_schema_request",
		"classify_browser_workflow",
		"check_schema_request_status",
	} {
		if !names[name] {
			t.Fatalf("missing MCP tool %s; got %#v", name, names)
		}
	}
}

func TestMCPInitializeAndToolsList(t *testing.T) {
	server := &mcpServer{}

	initResp := server.handle(t.Context(), mcpRequest{
		ID:     json.RawMessage(`1`),
		Method: "initialize",
	})
	initResult, ok := initResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("initialize result has wrong type: %#v", initResp.Result)
	}
	if initResult["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("unexpected protocol version: %#v", initResult["protocolVersion"])
	}

	listResp := server.handle(t.Context(), mcpRequest{
		ID:     json.RawMessage(`2`),
		Method: "tools/list",
	})
	listResult, ok := listResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("tools/list result has wrong type: %#v", listResp.Result)
	}
	tools, ok := listResult["tools"].([]mcpTool)
	if !ok || len(tools) != 5 {
		t.Fatalf("unexpected tools/list tools: %#v", listResult["tools"])
	}
}

func TestMCPStdioFraming(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	server := &mcpServer{
		in:  bufio.NewReader(strings.NewReader("Content-Length: " + strconv.Itoa(len(input)) + "\r\n\r\n" + input)),
		out: &bytes.Buffer{},
	}

	msg, err := server.readMessage()
	if err != nil {
		t.Fatalf("readMessage returned error: %v", err)
	}
	if string(msg) != input {
		t.Fatalf("unexpected message: %q", string(msg))
	}

	if err := server.write(mcpResponse{ID: json.RawMessage(`1`), Result: map[string]string{"ok": "true"}}); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	output := server.out.(*bytes.Buffer).String()
	if !strings.HasPrefix(output, "Content-Length: ") || !strings.Contains(output, "\r\n\r\n") {
		t.Fatalf("response is not MCP-framed: %q", output)
	}
}

func TestClassifyWorkflow(t *testing.T) {
	direct := classifyWorkflow("Fetch JSON prices from example.com API every hour and output price, id, and date fields.")
	if direct["likely_path"] != "direct_api" {
		t.Fatalf("expected direct_api, got %#v", direct)
	}

	owner := classifyWorkflow("Login to the private owner portal with OAuth and extract account status fields.")
	if owner["likely_path"] != "needs_owner" {
		t.Fatalf("expected needs_owner, got %#v", owner)
	}

	browserOnly := classifyWorkflow("My browser automation hits captcha and Cloudflare when clicking the dashboard.")
	if browserOnly["likely_path"] != "browser_only" {
		t.Fatalf("expected browser_only, got %#v", browserOnly)
	}
}

func TestSubmitSchemaRequestValidation(t *testing.T) {
	server := &mcpServer{}
	result := server.toolSubmitSchemaRequest(t.Context(), map[string]any{
		"domain":        "example.com",
		"task":          "Extract product prices",
		"read_or_write": "execute",
		"auth":          "public",
		"output_shape":  "price, sku",
		"failure_mode":  "selector drift",
	})
	if !result.IsError {
		t.Fatalf("expected invalid enum to return tool error")
	}
}

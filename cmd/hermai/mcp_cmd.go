package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/spf13/cobra"
)

const mcpProtocolVersion = "2025-06-18"

type mcpServer struct {
	apiBase string
	apiKey  string
	client  *http.Client
	in      *bufio.Reader
	out     io.Writer
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type mcpToolResult struct {
	Content           []map[string]string `json:"content"`
	StructuredContent any                 `json:"structuredContent,omitempty"`
	IsError           bool                `json:"isError,omitempty"`
}

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run Hermai as a Model Context Protocol server",
		Long: `Run Hermai as an MCP server so agent runtimes can discover, look up,
and submit schema requests without shelling out to individual commands.`,
	}
	cmd.AddCommand(newMCPServeCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	var apiBase string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve Hermai MCP tools over stdio",
		Long: `serve starts a stdio Model Context Protocol server.

Configure the registry API key with 'hermai registry login' or HERMAI_PLATFORM_KEY.
Read-only schema lookup tools also work anonymously within public rate limits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			if apiBase == "" {
				apiBase = cfg.Platform.URL
			}
			s := &mcpServer{
				apiBase: strings.TrimRight(apiBase, "/"),
				apiKey:  cfg.Platform.Key,
				client:  &http.Client{Timeout: 30 * time.Second},
				in:      bufio.NewReader(os.Stdin),
				out:     os.Stdout,
			}
			return s.serve(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base", "", "Hermai API base URL (default from config/HERMAI_PLATFORM_URL)")
	return cmd
}

func (s *mcpServer) serve(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := s.readMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		msg = bytes.TrimSpace(msg)
		if len(msg) == 0 {
			continue
		}

		var req mcpRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			_ = s.write(mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "parse error"}})
			continue
		}

		// Notifications have no id and do not receive responses.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}

		resp := s.handle(ctx, req)
		if err := s.write(resp); err != nil {
			return err
		}
	}
}

func (s *mcpServer) readMessage() ([]byte, error) {
	first, err := s.in.Peek(1)
	if err != nil {
		return nil, err
	}
	// Developer convenience: accept one JSON object per line when manually
	// testing from a shell. MCP clients use Content-Length framing below.
	if first[0] == '{' {
		return s.in.ReadBytes('\n')
	}

	contentLength := -1
	for {
		line, err := s.in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, errors.New("missing Content-Length header")
	}
	msg := make([]byte, contentLength)
	_, err = io.ReadFull(s.in, msg)
	return msg, err
}

func (s *mcpServer) write(resp mcpResponse) error {
	resp.JSONRPC = "2.0"
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = s.out.Write(data)
	return err
}

func (s *mcpServer) handle(ctx context.Context, req mcpRequest) mcpResponse {
	switch req.Method {
	case "initialize":
		return mcpResponse{
			ID: req.ID,
			Result: map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
				"serverInfo": map[string]string{
					"name":    "hermai-cli",
					"version": version,
				},
			},
		}
	case "tools/list":
		return mcpResponse{ID: req.ID, Result: map[string]any{"tools": hermaiMCPTools()}}
	case "tools/call":
		result, err := s.callTool(ctx, req.Params)
		if err != nil {
			return mcpResponse{ID: req.ID, Error: &mcpError{Code: -32602, Message: err.Error()}}
		}
		return mcpResponse{ID: req.ID, Result: result}
	default:
		return mcpResponse{ID: req.ID, Error: &mcpError{Code: -32601, Message: "method not found"}}
	}
}

func hermaiMCPTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "lookup_schema",
			Title:       "Look up a Hermai schema",
			Description: "Search Hermai for a schema by domain, task, category, or verification state. Read-only.",
			InputSchema: objectSchema(map[string]any{
				"domain":   stringProp("Exact domain, for example allbirds.com."),
				"task":     stringProp("Natural-language task or workflow description."),
				"category": stringProp("Optional schema category filter."),
				"verified": map[string]any{"type": "boolean", "description": "Only return verified schemas."},
			}, nil),
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		{
			Name:        "list_public_schemas",
			Title:       "List public Hermai schemas",
			Description: "List public schemas in the Hermai registry with optional filters. Read-only.",
			InputSchema: objectSchema(map[string]any{
				"q":        stringProp("Free-text search query."),
				"category": stringProp("Optional category filter."),
				"verified": map[string]any{"type": "boolean", "description": "Only return verified schemas."},
				"sort":     stringProp("Sort order, for example trending, recently_verified, or recent."),
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum number of schemas to return."},
			}, nil),
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		{
			Name:        "submit_schema_request",
			Title:       "Submit a schema request",
			Description: "Submit the six-field intake for a missing or brittle browser workflow. Never include cookies, API keys, or private session data.",
			InputSchema: objectSchema(map[string]any{
				"domain":          stringProp("Exact domain that agents need data from."),
				"task":            stringProp("Recurring task the agent is trying to perform."),
				"read_or_write":   enumProp([]string{"read", "write"}, "Whether the workflow reads data or performs a write action."),
				"auth":            enumProp([]string{"public", "authenticated"}, "Whether the workflow is public or requires owner-approved authentication."),
				"output_shape":    stringProp("Specific fields or JSON shape the agent needs."),
				"failure_mode":    stringProp("What breaks today: selector drift, timeout, captcha, stale values, API shape change, etc."),
				"source_url":      stringProp("Optional public thread, issue, or page where this request came from."),
				"requester_agent": stringProp("Optional agent or builder identifier."),
				"contact":         stringProp("Optional contact. Do not include secrets."),
				"idempotency_key": stringProp("Optional stable key for retry-safe submits."),
			}, []string{"domain", "task", "read_or_write", "auth", "output_shape", "failure_mode"}),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true},
		},
		{
			Name:        "classify_browser_workflow",
			Title:       "Classify a browser workflow",
			Description: "Classify a prose workflow as direct API, hidden endpoint, browser-only, or needs owner/auth. Read-only and local.",
			InputSchema: objectSchema(map[string]any{
				"prose": stringProp("Post, issue, or user request describing the workflow."),
			}, []string{"prose"}),
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
		{
			Name:        "check_schema_request_status",
			Title:       "Check schema request status",
			Description: "Check the status of a previously submitted Hermai schema request. Read-only.",
			InputSchema: objectSchema(map[string]any{
				"request_id": stringProp("Schema request id returned by submit_schema_request."),
			}, []string{"request_id"}),
			Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
		},
	}
}

func (s *mcpServer) callTool(ctx context.Context, paramsRaw json.RawMessage) (mcpToolResult, error) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return mcpToolResult{}, fmt.Errorf("invalid tools/call params: %w", err)
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	switch params.Name {
	case "lookup_schema":
		return s.toolLookupSchema(ctx, params.Arguments), nil
	case "list_public_schemas":
		return s.toolListPublicSchemas(ctx, params.Arguments), nil
	case "submit_schema_request":
		return s.toolSubmitSchemaRequest(ctx, params.Arguments), nil
	case "classify_browser_workflow":
		return toolClassifyBrowserWorkflow(params.Arguments), nil
	case "check_schema_request_status":
		return s.toolCheckSchemaRequestStatus(ctx, params.Arguments), nil
	default:
		return mcpToolResult{}, fmt.Errorf("unknown tool: %s", params.Name)
	}
}

func (s *mcpServer) toolLookupSchema(ctx context.Context, args map[string]any) mcpToolResult {
	domain := strings.ToLower(strings.TrimSpace(asString(args["domain"])))
	task := strings.TrimSpace(asString(args["task"]))
	category := strings.TrimSpace(asString(args["category"]))

	if domain != "" && task == "" && category == "" {
		return s.apiTool(ctx, http.MethodGet, "/v1/schemas/"+url.PathEscape(domain), nil, false, nil)
	}

	q := url.Values{}
	if domain != "" {
		q.Set("q", domain)
	}
	if task != "" {
		if q.Get("q") != "" {
			q.Set("q", q.Get("q")+" "+task)
		} else {
			q.Set("q", task)
		}
	}
	if category != "" {
		q.Set("category", category)
	}
	if v, ok := args["verified"].(bool); ok {
		q.Set("verified", fmt.Sprintf("%t", v))
	}
	path := "/v1/schemas"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return s.apiTool(ctx, http.MethodGet, path, nil, false, nil)
}

func (s *mcpServer) toolListPublicSchemas(ctx context.Context, args map[string]any) mcpToolResult {
	q := url.Values{}
	for _, key := range []string{"q", "category", "sort"} {
		if value := strings.TrimSpace(asString(args[key])); value != "" {
			q.Set(key, value)
		}
	}
	if v, ok := args["verified"].(bool); ok {
		q.Set("verified", fmt.Sprintf("%t", v))
	}
	if limit := asInt(args["limit"]); limit > 0 {
		if limit > 50 {
			limit = 50
		}
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/v1/schemas"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return s.apiTool(ctx, http.MethodGet, path, nil, false, nil)
}

func (s *mcpServer) toolSubmitSchemaRequest(ctx context.Context, args map[string]any) mcpToolResult {
	for _, key := range []string{"domain", "task", "read_or_write", "auth", "output_shape", "failure_mode"} {
		if strings.TrimSpace(asString(args[key])) == "" {
			return textToolError(fmt.Sprintf("missing required field: %s", key))
		}
	}
	if !validEnum(asString(args["read_or_write"]), "read", "write") {
		return textToolError("read_or_write must be read or write")
	}
	if !validEnum(asString(args["auth"]), "public", "authenticated") {
		return textToolError("auth must be public or authenticated")
	}

	body := map[string]any{
		"domain":        strings.ToLower(strings.TrimSpace(asString(args["domain"]))),
		"task":          strings.TrimSpace(asString(args["task"])),
		"read_or_write": strings.TrimSpace(asString(args["read_or_write"])),
		"auth":          strings.TrimSpace(asString(args["auth"])),
		"output_shape":  strings.TrimSpace(asString(args["output_shape"])),
		"failure_mode":  strings.TrimSpace(asString(args["failure_mode"])),
		"source":        "mcp",
	}
	for _, key := range []string{"source_url", "requester_agent", "contact"} {
		if value := strings.TrimSpace(asString(args[key])); value != "" {
			body[key] = value
		}
	}
	headers := map[string]string{"Idempotency-Key": idempotencyKey(args)}
	return s.apiTool(ctx, http.MethodPost, "/v1/schema-requests", body, false, headers)
}

func toolClassifyBrowserWorkflow(args map[string]any) mcpToolResult {
	prose := strings.TrimSpace(asString(args["prose"]))
	if prose == "" {
		return textToolError("prose is required")
	}
	classification := classifyWorkflow(prose)
	return textToolResult("Workflow classification: "+classification["likely_path"].(string), classification)
}

func (s *mcpServer) toolCheckSchemaRequestStatus(ctx context.Context, args map[string]any) mcpToolResult {
	requestID := strings.TrimSpace(asString(args["request_id"]))
	if requestID == "" {
		return textToolError("request_id is required")
	}
	return s.apiTool(ctx, http.MethodGet, "/v1/schema-requests/"+url.PathEscape(requestID), nil, false, nil)
}

func (s *mcpServer) apiTool(ctx context.Context, method, path string, body any, auth bool, extraHeaders map[string]string) mcpToolResult {
	data, err := s.callHermaiAPI(ctx, method, path, body, auth, extraHeaders)
	if err != nil {
		return textToolError(err.Error())
	}
	return textToolResult("Hermai API response", data)
}

func (s *mcpServer) callHermaiAPI(ctx context.Context, method, path string, body any, auth bool, extraHeaders map[string]string) (any, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.apiBase+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	} else if auth {
		return nil, errors.New("Hermai API key required; run hermai registry login or set HERMAI_PLATFORM_KEY")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var decoded any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decoding Hermai response status %d: %w", resp.StatusCode, err)
		}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Hermai API %s %s returned %d: %s", method, path, resp.StatusCode, string(raw))
	}

	if env, ok := decoded.(map[string]any); ok {
		if success, hasSuccess := env["success"].(bool); hasSuccess {
			if !success {
				return nil, fmt.Errorf("Hermai API error: %v", env["error"])
			}
			if data, ok := env["data"]; ok {
				return data, nil
			}
		}
	}
	return decoded, nil
}

func classifyWorkflow(prose string) map[string]any {
	lower := strings.ToLower(prose)
	score := map[string]int{
		"direct_api":      countMatches(lower, `\b(api|json|graphql|rss|sitemap|openapi|endpoint|xhr|network tab)\b`),
		"hidden_endpoint": countMatches(lower, `\b(scrap|selector|dom|html|browser automation|response shape|field disappeared|drift|parse|null)\b`),
		"needs_owner":     countMatches(lower, `\b(login|authenticated|private|cookie|oauth|portal|owner|credential|session)\b`),
		"browser_only":    countMatches(lower, `\b(captcha|recaptcha|datadome|cloudflare|click|canvas|webgl|human verification)\b`),
	}
	likely := "needs_more_fields"
	best := 0
	for _, key := range []string{"needs_owner", "browser_only", "direct_api", "hidden_endpoint"} {
		if score[key] > best {
			best = score[key]
			likely = key
		}
	}
	missing := missingIntakeFields(lower)
	return map[string]any{
		"likely_path":           likely,
		"scores":                score,
		"missing_intake_fields": missing,
		"next_question":         nextIntakeQuestion(missing),
		"safety_note":           "Never include cookies, API keys, bearer tokens, or private session data in public schema requests.",
	}
}

func missingIntakeFields(lower string) []string {
	missing := []string{}
	if !regexp.MustCompile(`\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9][a-z0-9-]*)+\.[a-z]{2,}\b`).MatchString(lower) {
		missing = append(missing, "domain")
	}
	if !regexp.MustCompile(`\b(read|write|fetch|extract|monitor|post|update|submit|create|delete)\b`).MatchString(lower) {
		missing = append(missing, "read_or_write")
	}
	if !regexp.MustCompile(`\b(public|authenticated|login|private|cookie|oauth|api key|credential)\b`).MatchString(lower) {
		missing = append(missing, "auth")
	}
	if !regexp.MustCompile(`\b(fields?|json|columns?|output|schema|price|name|status|id|url|date|total)\b`).MatchString(lower) {
		missing = append(missing, "output_shape")
	}
	if !regexp.MustCompile(`\b(broke|break|drift|timeout|captcha|selector|null|stale|failed|failure|rate limit|403|429|500)\b`).MatchString(lower) {
		missing = append(missing, "failure_mode")
	}
	return missing
}

func nextIntakeQuestion(missing []string) string {
	if len(missing) == 0 {
		return "The intake looks complete enough to submit as a schema request."
	}
	if len(missing) > 3 {
		missing = missing[:3]
	}
	return "Ask for: " + strings.Join(missing, ", ") + "."
}

func countMatches(text, pattern string) int {
	return len(regexp.MustCompile(pattern).FindAllString(text, -1))
}

func textToolResult(text string, structured any) mcpToolResult {
	pretty, err := json.MarshalIndent(structured, "", "  ")
	if err == nil && len(pretty) > 0 {
		text += "\n\n" + string(pretty)
	}
	return mcpToolResult{
		Content:           []map[string]string{{"type": "text", "text": text}},
		StructuredContent: structured,
	}
}

func textToolError(message string) mcpToolResult {
	return mcpToolResult{
		Content: []map[string]string{{"type": "text", "text": message}},
		IsError: true,
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumProp(values []string, description string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": description}
}

func asString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func asInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func validEnum(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

func idempotencyKey(args map[string]any) string {
	if key := strings.TrimSpace(asString(args["idempotency_key"])); key != "" {
		return key
	}
	data, _ := json.Marshal(args)
	sum := sha256.Sum256(data)
	return "mcp_" + hex.EncodeToString(sum[:])[:32]
}

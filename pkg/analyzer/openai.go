package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/browser"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// OpenAIConfig holds configuration for the OpenAI-compatible LLM client.
type OpenAIConfig struct {
	BaseURL       string
	APIKey        string
	Model         string
	ClassifyModel string // fast/cheap model for HAR classification (optional, defaults to Model)
}

// OpenAIAnalyzer implements the Service interface using an OpenAI-compatible API.
type OpenAIAnalyzer struct {
	config OpenAIConfig
	client *http.Client
}

// NewOpenAIAnalyzer creates a new OpenAIAnalyzer with the given configuration.
func NewOpenAIAnalyzer(config OpenAIConfig) *OpenAIAnalyzer {
	return &OpenAIAnalyzer{
		config: config,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type endpointsWrapper struct {
	Endpoints []schema.Endpoint     `json:"endpoints"`
	Session   *schema.SessionConfig `json:"session,omitempty"`
}

// Analyze implements the Service interface. It sends the HAR log, DOM snapshot,
// and original URL to the configured LLM and parses the response into a Schema.
// Uses the fast ClassifyModel (if configured) since HAR classification is a
// simple NOISE/CANDIDATE task that doesn't need an expensive model.
func (a *OpenAIAnalyzer) Analyze(ctx context.Context, har *browser.HARLog, dom string, originalURL string) (*schema.Schema, error) {
	userPrompt := BuildPrompt(har, dom, originalURL)

	result, err := a.callLLMFull(ctx, a.classifyModel(), SystemPrompt(), userPrompt)
	if err != nil {
		return nil, err
	}

	s, err := buildSchemaFromEndpoints(originalURL, result.Endpoints)
	if err != nil {
		return nil, err
	}
	s.Session = result.Session
	return s, nil
}

// classifyModel returns the model to use for HAR classification.
// Falls back to the main model if no classify model is configured.
func (a *OpenAIAnalyzer) classifyModel() string {
	if a.config.ClassifyModel != "" {
		return a.config.ClassifyModel
	}
	return a.config.Model
}

// Suggest uses LLM knowledge to suggest public API endpoints when browser capture fails.
func (a *OpenAIAnalyzer) Suggest(ctx context.Context, originalURL string, failureReason string) (*schema.Schema, error) {
	userPrompt := BuildSuggestPrompt(originalURL, failureReason)

	endpoints, err := a.callLLM(ctx, a.config.Model, SuggestSystemPrompt(), userPrompt)
	if err != nil {
		return nil, err
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("LLM has no public API suggestions for %s", originalURL)
	}

	return buildSchemaFromEndpoints(originalURL, endpoints)
}

// AnalyzeHTML examines raw HTML to identify CSS selectors for content extraction.
func (a *OpenAIAnalyzer) AnalyzeHTML(ctx context.Context, rawHTML string, originalURL string) (*schema.ExtractionRules, error) {
	userPrompt := BuildHTMLExtractionPrompt(rawHTML, originalURL)

	rules, err := a.callLLMForRules(ctx, HTMLExtractionSystemPrompt(), userPrompt)
	if err != nil {
		return nil, err
	}

	if rules.ContentSelector == "" {
		return nil, fmt.Errorf("LLM returned empty content_selector for %s", originalURL)
	}

	return rules, nil
}

// AnalyzeNextDataPaths examines __NEXT_DATA__ pageProps and identifies named
// extraction paths so subsequent fetches return targeted sub-trees.
func (a *OpenAIAnalyzer) AnalyzeNextDataPaths(ctx context.Context, pageProps map[string]any, originalURL string) (map[string]string, error) {
	userPrompt := BuildNextDataPathsPrompt(pageProps, originalURL)
	model := a.classifyModel()

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: NextDataPathsSystemPrompt()},
			{Role: "user", Content: userPrompt},
		},
		ResponseFormat: responseFormat{Type: "json_object"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := a.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.config.APIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResp = 1 * 1024 * 1024
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResp))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no choices")
	}

	content := stripCodeFences(chatResp.Choices[0].Message.Content)
	content = repairJSON(content)

	var result struct {
		Paths map[string]string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse NextDataPaths response: %w", err)
	}

	return result.Paths, nil
}

// Ask sends data + a natural language prompt to the fast classify model
// and returns a plain text answer. The data is truncated to fit context.
func (a *OpenAIAnalyzer) Ask(ctx context.Context, data string, prompt string) (string, error) {
	// Truncate data to avoid exceeding context window
	const maxDataLen = 30000
	if len(data) > maxDataLen {
		data = data[:maxDataLen] + "\n...[truncated]"
	}

	systemPrompt := `You are a data extraction assistant. The user will give you data from a web page or API response, plus a question. Answer the question concisely based only on the provided data. If the data doesn't contain the answer, say so. Output plain text, not JSON.`

	userPrompt := fmt.Sprintf("DATA:\n%s\n\nQUESTION: %s", data, prompt)

	model := a.classifyModel()

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := a.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.config.APIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResp = 1 * 1024 * 1024
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResp))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}

type extractionRulesWrapper struct {
	ExtractionRules schema.ExtractionRules `json:"extraction_rules"`
}

// callLLMForRules calls the LLM and parses the response as extraction rules.
func (a *OpenAIAnalyzer) callLLMForRules(ctx context.Context, systemPrompt, userPrompt string) (*schema.ExtractionRules, error) {
	var lastErr error

	for attempt := 0; attempt < maxLLMRetries; attempt++ {
		rules, err := a.doLLMCallForRules(ctx, systemPrompt, userPrompt)
		if err == nil {
			return rules, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return nil, lastErr
}

func (a *OpenAIAnalyzer) doLLMCallForRules(ctx context.Context, systemPrompt, userPrompt string) (*schema.ExtractionRules, error) {
	reqBody := chatRequest{
		Model: a.config.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		ResponseFormat: responseFormat{Type: "json_object"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := a.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.config.APIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxLLMResponse = 1 * 1024 * 1024
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxLLMResponse))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no choices")
	}

	content := stripCodeFences(chatResp.Choices[0].Message.Content)
	content = repairJSON(content)

	var wrapper extractionRulesWrapper
	if err := json.Unmarshal([]byte(content), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to parse extraction rules: %w", err)
	}

	return &wrapper.ExtractionRules, nil
}

// buildSchemaFromEndpoints constructs a Schema from a URL and discovered endpoints.
func buildSchemaFromEndpoints(originalURL string, endpoints []schema.Endpoint) (*schema.Schema, error) {
	parsed, err := url.Parse(originalURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	domain, err := schema.ExtractDomain(originalURL)
	if err != nil {
		return nil, fmt.Errorf("failed to extract domain: %w", err)
	}

	urlPattern := improvePattern(parsed.Path, endpoints)
	schemaID := schema.GenerateID(domain, urlPattern)

	return &schema.Schema{
		ID:             schemaID,
		Domain:         domain,
		URLPattern:     urlPattern,
		SchemaType:     schema.SchemaTypeAPI,
		Version:        1,
		CreatedAt:      time.Now(),
		DiscoveredFrom: originalURL,
		Endpoints:      normalizeEndpoints(endpoints),
	}, nil
}

// maxLLMRetries is the number of times to retry on empty/broken LLM responses.
const maxLLMRetries = 2

// callLLM sends a system+user prompt to the LLM and parses the endpoint response.
// Retries once if the LLM returns empty or unparseable content.
func (a *OpenAIAnalyzer) callLLM(ctx context.Context, model, systemPrompt, userPrompt string) ([]schema.Endpoint, error) {
	result, err := a.callLLMFull(ctx, model, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}
	return result.Endpoints, nil
}

// callLLMFull returns the full parsed result including session config.
func (a *OpenAIAnalyzer) callLLMFull(ctx context.Context, model, systemPrompt, userPrompt string) (*llmResult, error) {
	var lastErr error

	for attempt := 0; attempt < maxLLMRetries; attempt++ {
		result, err := a.doLLMCallFull(ctx, model, systemPrompt, userPrompt)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return nil, lastErr
}

func (a *OpenAIAnalyzer) doLLMCallFull(ctx context.Context, model, systemPrompt, userPrompt string) (*llmResult, error) {
	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		ResponseFormat: responseFormat{Type: "json_object"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := a.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.config.APIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResp = 1 * 1024 * 1024
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResp))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	return parseLLMResponse(respBytes)
}

func (a *OpenAIAnalyzer) doLLMCall(ctx context.Context, systemPrompt, userPrompt string) ([]schema.Endpoint, error) {
	reqBody := chatRequest{
		Model: a.config.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		ResponseFormat: responseFormat{Type: "json_object"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := a.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.config.APIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxLLMResponse = 1 * 1024 * 1024 // 1MB
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxLLMResponse))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	return parseEndpointsResponse(respBytes)
}

// llmResult holds the parsed output from the LLM's endpoint analysis.
type llmResult struct {
	Endpoints []schema.Endpoint
	Session   *schema.SessionConfig
}

// parseEndpointsResponse extracts endpoints and session config from an LLM response.
func parseEndpointsResponse(respBytes []byte) ([]schema.Endpoint, error) {
	parsed, err := parseLLMResponse(respBytes)
	if err != nil {
		return nil, err
	}
	return parsed.Endpoints, nil
}

func parseLLMResponse(respBytes []byte) (*llmResult, error) {
	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no choices")
	}

	content := stripCodeFences(chatResp.Choices[0].Message.Content)
	content = repairJSON(content)

	var wrapper endpointsWrapper
	if err := json.Unmarshal([]byte(content), &wrapper); err != nil {
		preview := content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("hermai: LLM failed to extract schema: %w\nLLM output preview: %s", err, preview)
	}

	return &llmResult{
		Endpoints: normalizeEndpoints(wrapper.Endpoints),
		Session:   wrapper.Session,
	}, nil
}

func normalizeEndpoints(endpoints []schema.Endpoint) []schema.Endpoint {
	normalized := make([]schema.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Headers == nil {
			endpoint.Headers = map[string]string{}
		}
		if endpoint.Description == "" {
			endpoint.Description = describeEndpointName(endpoint.Name)
		}
		if endpoint.Confidence <= 0 {
			endpoint.Confidence = 0.65
		}
		normalized = append(normalized, endpoint)
	}
	return normalized
}

func describeEndpointName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "Endpoint discovered from browser traffic"
	}
	return strings.ToUpper(name[:1]) + name[1:] + "."
}

// improvePattern uses LLM-discovered variables to create a better URL pattern.
// If the LLM identified path variables, we match them against the original path
// segments and replace matching segments with {} for better cache reuse.
func improvePattern(path string, endpoints []schema.Endpoint) string {
	pattern := schema.NormalizePathStructure(path)

	origSegments := strings.Split(strings.Trim(path, "/"), "/")
	patSegments := strings.Split(strings.Trim(pattern, "/"), "/")

	if len(origSegments) != len(patSegments) {
		return pattern
	}

	for _, ep := range endpoints {
		for _, v := range ep.Variables {
			if v.Source != "path" && v.Source != "url" {
				continue
			}

			re, err := regexp.Compile(v.Pattern)
			if err != nil {
				continue
			}

			// Search from end — dynamic values are usually the last segments
			for i := len(origSegments) - 1; i >= 0; i-- {
				if patSegments[i] == "{}" {
					continue
				}
				if re.MatchString(origSegments[i]) {
					patSegments[i] = "{}"
					break
				}
			}
		}
	}

	return "/" + strings.Join(patSegments, "/")
}

// stripCodeFences removes markdown code fences from LLM output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// repairJSON attempts to fix common truncated JSON issues from LLMs.
func repairJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	braces := 0
	brackets := 0
	inString := false
	escaped := false

	for _, c := range s {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			braces++
		case '}':
			braces--
		case '[':
			brackets++
		case ']':
			brackets--
		}
	}

	if inString {
		s += `"`
	}

	for brackets > 0 {
		s += "]"
		brackets--
	}
	for braces > 0 {
		s += "}"
		braces--
	}

	return s
}

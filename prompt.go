package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	systemPrompt = `You are a friendly shell-command assistant. You help the user find the right terminal command through conversation.

BEHAVIOUR:
1. When the user describes what they want, reply with the shell command inside a fenced code block like this:
   ` + "```" + `
   command here
   ` + "```" + `
2. You may include a SHORT explanation (1-2 sentences max) outside the code block.
3. If the request is ambiguous, ask a clarifying question INSTEAD of guessing.
4. If the command is DANGEROUS (rm -rf, dd, mkfs, chmod 777, kill -9, format, drop database, etc.), you MUST:
   - Still show the command in a code block.
   - Warn the user clearly that this is destructive.
   - Ask: "Are you sure you want to run this?"
   - Prefix the code block command with: DANGER: 
5. If the user confirms a dangerous command (says yes, sure, do it, confirmed, etc.), reply with ONLY the command in a code block (no DANGER prefix this time). This is the final answer.
6. If the user says no or wants changes, continue the conversation.
7. For safe commands where you are confident, just give the command directly.
8. Keep replies concise. This is a chat, not documentation.

EXAMPLES:

User: list files in current directory
Assistant: ` + "```" + `
ls -la
` + "```" + `

User: delete everything in /tmp
Assistant: This will permanently remove all files and directories in /tmp.

` + "```" + `
DANGER: rm -rf /tmp/*
` + "```" + `

Are you sure you want to run this?

User: yes
Assistant: ` + "```" + `
rm -rf /tmp/*
` + "```" + `

User: what port is my server on
Assistant: Could you clarify — do you want to see which process is listening on a specific port, or list all open ports?`

	defaultLMStudioBaseURL = "http://127.0.0.1:1234/v1"
	defaultModel           = "mlx-community/Phi-4-mini-instruct-4bit"
	maxHistoryMessages     = 40

	ProviderLMStudio     = "lmstudio"
	ProviderGoogle       = "google"
	defaultGoogleBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	defaultGoogleModel   = "gemini-2.0-flash"
)

// ErrGoogleAPIKeyRequired is returned when the Google provider is used without any API key.
var ErrGoogleAPIKeyRequired = errors.New("google API key required: set GOOGLE_API_KEY on the server or enter a key in the UI")

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
}

type completionMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Refusal          string          `json:"refusal,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      completionMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
		Text         string            `json:"text,omitempty"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ChatReply is the structured result returned to the HTTP handler.
type ChatReply struct {
	Text      string // full assistant message (may contain markdown)
	Command   string // extracted shell command, if any
	Dangerous bool   // true when the assistant flagged the command as dangerous
}

var (
	reCodeFence  = regexp.MustCompile("(?s)```[a-z]*\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
)

func lmBaseURL() string {
	base := strings.TrimRight(os.Getenv("LM_STUDIO_BASE_URL"), "/")
	if base == "" {
		base = strings.TrimRight(defaultLMStudioBaseURL, "/")
	}
	return base
}

func googleBaseURL() string {
	if base := strings.TrimRight(os.Getenv("GOOGLE_AI_BASE_URL"), "/"); base != "" {
		return base
	}
	return defaultGoogleBaseURL
}

func googleAPIKey() string {
	return os.Getenv("GOOGLE_API_KEY")
}

// GoogleKeyConfigured reports whether the process has a Google key from the environment.
func GoogleKeyConfigured() bool {
	return strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")) != ""
}

// effectiveGoogleKey uses override when non-empty, otherwise the environment variable.
func effectiveGoogleKey(override string) string {
	if k := strings.TrimSpace(override); k != "" {
		return k
	}
	return googleAPIKey()
}

func providerBaseURL(provider string) string {
	if provider == ProviderGoogle {
		return googleBaseURL()
	}
	return lmBaseURL()
}

func effectiveModel(override, provider string) string {
	if o := strings.TrimSpace(override); o != "" {
		return o
	}
	if provider == ProviderGoogle {
		if m := strings.TrimSpace(os.Getenv("GOOGLE_MODEL")); m != "" {
			return m
		}
		return defaultGoogleModel
	}
	if m := strings.TrimSpace(os.Getenv("LM_STUDIO_MODEL")); m != "" {
		return m
	}
	return defaultModel
}

func setLMStudioAuth(req *http.Request) {
	if key := os.Getenv("LM_STUDIO_API_KEY"); key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
}

func setProviderAuth(req *http.Request, provider, googleKeyOverride string) {
	if provider == ProviderGoogle {
		if key := effectiveGoogleKey(googleKeyOverride); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		return
	}
	setLMStudioAuth(req)
}

// converse sends the full conversation to the LLM and parses the reply.
// googleKeyOverride is optional; used when GOOGLE_API_KEY is not set (e.g. browser-supplied key).
func converse(history []chatMessage, userInput, modelOverride, provider, googleKeyOverride string) (*ChatReply, error) {
	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return nil, fmt.Errorf("empty user message")
	}

	hist := normalizeHistory(history)
	msgs := make([]chatMessage, 0, 2+len(hist))
	msgs = append(msgs, chatMessage{Role: "system", Content: systemPrompt})
	msgs = append(msgs, hist...)
	msgs = append(msgs, chatMessage{Role: "user", Content: userInput})

	rawText, err := chatCompletion(msgs, modelOverride, provider, googleKeyOverride)
	if err != nil {
		return nil, err
	}

	reply := &ChatReply{Text: rawText}

	// Extract command from fenced code blocks
	if m := reCodeFence.FindStringSubmatch(rawText); len(m) > 1 {
		cmd := strings.TrimSpace(m[1])
		if strings.HasPrefix(strings.ToUpper(cmd), "DANGER:") {
			reply.Dangerous = true
			cmd = strings.TrimSpace(cmd[len("DANGER:"):])
		}
		reply.Command = cmd
	}

	// Also detect DANGER in the prose
	if !reply.Dangerous {
		lower := strings.ToLower(rawText)
		if strings.Contains(lower, "danger") || strings.Contains(lower, "destructive") || strings.Contains(lower, "are you sure") {
			if reply.Command != "" {
				reply.Dangerous = true
			}
		}
	}

	return reply, nil
}

// translate is the CLI entry point (unchanged signature for main.go).
func translate(userInput string) (string, error) {
	r, err := converse(nil, userInput, "", "", "")
	if err != nil {
		return "", err
	}
	if r.Command != "" {
		return r.Command, nil
	}
	return r.Text, nil
}

func normalizeHistory(h []chatMessage) []chatMessage {
	var out []chatMessage
	for _, m := range h {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		out = append(out, chatMessage{Role: role, Content: c})
	}
	if len(out) > maxHistoryMessages {
		out = out[len(out)-maxHistoryMessages:]
	}
	return out
}

func chatCompletion(messages []chatMessage, modelOverride, provider, googleKeyOverride string) (string, error) {
	baseURL := providerBaseURL(provider)
	model := effectiveModel(modelOverride, provider)

	if provider == ProviderGoogle && effectiveGoogleKey(googleKeyOverride) == "" {
		return "", ErrGoogleAPIKeyRequired
	}

	reqBody := chatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   4096,
		Temperature: 0.4,
		Stream:      false,
	}
	if v := strings.TrimSpace(os.Getenv("LM_STUDIO_MAX_TOKENS")); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			reqBody.MaxTokens = n
		}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	setProviderAuth(req, provider, googleKeyOverride)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result chatResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		log.Printf("lmstudio: decode response: %v body=%s", err, truncate(string(raw), 500))
		return "", fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(raw), 200))
	}

	if result.Error != nil {
		log.Printf("lmstudio: API error: %s (type=%s) model=%q", result.Error.Message, result.Error.Type, model)
		return "", fmt.Errorf("API error: %s", result.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("lmstudio: HTTP %s model=%q body=%s", resp.Status, model, truncate(string(raw), 500))
		return "", fmt.Errorf("HTTP %s: %s", resp.Status, truncate(string(raw), 200))
	}
	if len(result.Choices) == 0 {
		log.Printf("lmstudio: no choices model=%q body=%s", model, truncate(string(raw), 500))
		return "", fmt.Errorf("empty choices in response")
	}

	ch0 := result.Choices[0]
	if r := strings.TrimSpace(ch0.Message.Refusal); r != "" {
		return "", fmt.Errorf("model refused: %s", r)
	}

	out := messageContentText(ch0.Message.Content)
	if out == "" {
		out = strings.TrimSpace(ch0.Text)
	}

	// Fallback: reasoning models
	if out == "" {
		if rc := strings.TrimSpace(ch0.Message.ReasoningContent); rc != "" {
			out = extractFromReasoning(rc)
			if out != "" {
				log.Printf("lmstudio: used reasoning_content fallback model=%q", model)
			}
		}
	}

	if out == "" {
		hint := ""
		if ch0.FinishReason == "length" {
			hint = " (model ran out of tokens)"
		}
		return "", fmt.Errorf("empty response from %q%s", model, hint)
	}
	return out, nil
}

func messageContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil && len(parts) > 0 {
		var b strings.Builder
		for _, p := range parts {
			if strings.EqualFold(p.Type, "text") && p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func extractFromReasoning(reasoning string) string {
	if matches := reCodeFence.FindAllStringSubmatch(reasoning, -1); len(matches) > 0 {
		for i := len(matches) - 1; i >= 0; i-- {
			c := strings.TrimSpace(matches[i][1])
			if c != "" {
				return c
			}
		}
	}
	if matches := reInlineCode.FindAllStringSubmatch(reasoning, -1); len(matches) > 0 {
		for i := len(matches) - 1; i >= 0; i-- {
			c := strings.TrimSpace(matches[i][1])
			if c != "" {
				return c
			}
		}
	}
	return ""
}

// --- Models list ---

func parseModelsJSON(raw []byte) ([]string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	if errRaw, ok := top["error"]; ok && len(errRaw) > 0 && string(errRaw) != "null" {
		var errObj struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(errRaw, &errObj)
		if errObj.Message != "" {
			return nil, fmt.Errorf("API error: %s", errObj.Message)
		}
	}
	dataRaw, ok := top["data"]
	if !ok || len(dataRaw) == 0 || string(dataRaw) == "null" {
		return nil, nil
	}

	var asObjs []map[string]interface{}
	if err := json.Unmarshal(dataRaw, &asObjs); err != nil {
		return nil, fmt.Errorf("decode models data: %w", err)
	}

	seen := make(map[string]struct{})
	var out []string
	for _, o := range asObjs {
		var id string
		for _, key := range []string{"id", "model", "name"} {
			if v, ok := o[key]; ok && v != nil {
				if s, ok := v.(string); ok {
					id = strings.TrimSpace(s)
					break
				}
			}
		}
		if id == "" {
			continue
		}
		id = strings.TrimPrefix(id, "models/")
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func ListModels(provider, googleKeyOverride string) ([]string, error) {
	if provider == ProviderGoogle && effectiveGoogleKey(googleKeyOverride) == "" {
		return nil, ErrGoogleAPIKeyRequired
	}

	base := providerBaseURL(provider)
	req, err := http.NewRequest("GET", base+"/models", nil)
	if err != nil {
		return nil, err
	}
	setProviderAuth(req, provider, googleKeyOverride)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, truncate(string(raw), 200))
	}
	ids, err := parseModelsJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w (body: %s)", err, truncate(string(raw), 200))
	}
	return ids, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

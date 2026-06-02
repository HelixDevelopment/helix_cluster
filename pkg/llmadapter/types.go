// Package llmadapter provides request/response shape adapters that translate
// a Claude Messages request and an OpenAI Responses-API request into a common
// OpenAI chat-completions request, and translate the OpenAI response back into
// the client-native shape, preserving tool-call ordering.
package llmadapter

import "encoding/json"

// ---------------------------------------------------------------------------
// Claude Messages API shapes
// ---------------------------------------------------------------------------

// ClaudeContentBlock represents a single content block in a Claude message.
// Type is one of "text", "tool_use", "tool_result".
type ClaudeContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ClaudeMessage is a single turn in the Claude conversation.
type ClaudeMessage struct {
	Role    string               `json:"role"`
	Content []ClaudeContentBlock `json:"content"`
}

// ClaudeTool describes a tool available to the model.
type ClaudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ClaudeRequest is a Claude Messages API request body.
type ClaudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []ClaudeMessage `json:"messages"`
	Tools     []ClaudeTool    `json:"tools,omitempty"`
}

// ClaudeResponse is the Claude Messages API response body.
type ClaudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Model        string               `json:"model"`
	Content      []ClaudeContentBlock `json:"content"`
	StopReason   string               `json:"stop_reason"`
	StopSequence *string              `json:"stop_sequence"`
	Usage        ClaudeUsage          `json:"usage"`
	RunID        string               `json:"run_id,omitempty"`
}

// ClaudeUsage carries token-count telemetry.
type ClaudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ---------------------------------------------------------------------------
// OpenAI Responses-API shapes (simplified subset)
// ---------------------------------------------------------------------------

// ResponsesAPIRequest is the shape of an OpenAI Responses-API request.
type ResponsesAPIRequest struct {
	Model     string                  `json:"model"`
	Input     []ResponsesAPIInputItem `json:"input"`
	MaxTokens int                     `json:"max_output_tokens,omitempty"`
}

// ResponsesAPIInputItem is a single item in the Responses-API input array.
type ResponsesAPIInputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// ---------------------------------------------------------------------------
// OpenAI Chat Completions shapes
// ---------------------------------------------------------------------------

// OpenAIChatMessage is a single chat message.
type OpenAIChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// OpenAIToolCall is a single tool call within an assistant message.
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall holds the name and serialised arguments.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool describes a function tool in the chat request.
type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIToolFunction `json:"function"`
}

// OpenAIToolFunction is the function descriptor within an OpenAI tool.
type OpenAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// OpenAIChatRequest is the OpenAI Chat Completions request body.
type OpenAIChatRequest struct {
	Model     string              `json:"model"`
	Messages  []OpenAIChatMessage `json:"messages"`
	MaxTokens int                 `json:"max_tokens,omitempty"`
	Tools     []OpenAITool        `json:"tools,omitempty"`
}

// OpenAIChatChoice is one completion choice.
type OpenAIChatChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

// OpenAIChatUsage carries token counts in the response.
type OpenAIChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIChatResponse is the OpenAI Chat Completions response body.
type OpenAIChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []OpenAIChatChoice `json:"choices"`
	Usage   OpenAIChatUsage    `json:"usage"`
	RunID   string             `json:"run_id,omitempty"`
}

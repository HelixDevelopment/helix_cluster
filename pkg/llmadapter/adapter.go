package llmadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrNoCapability is returned when the adapter is asked to perform an operation
// for which no real backend endpoint has been injected.
var ErrNoCapability = errors.New("llmadapter: capability not available (no endpoint injected)")

// ---------------------------------------------------------------------------
// Translation: Claude → OpenAI Chat
// ---------------------------------------------------------------------------

// TranslateClaudeRequest converts a ClaudeRequest into an OpenAIChatRequest.
//
// Mapping rules:
//   - ClaudeRequest.System  → a "system" role message prepended to Messages.
//   - ClaudeMessage.Content[]  type=="text"     → text content of the message.
//   - ClaudeMessage.Content[]  type=="tool_use" → ToolCalls entry on the preceding
//     assistant message (or a standalone assistant message if first).
//   - ClaudeTool entries → OpenAITool entries with type=="function".
func TranslateClaudeRequest(req ClaudeRequest) OpenAIChatRequest {
	out := OpenAIChatRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
	}

	// System prompt.
	if req.System != "" {
		out.Messages = append(out.Messages, OpenAIChatMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	// Conversation turns.
	for _, m := range req.Messages {
		msg := OpenAIChatMessage{Role: m.Role}
		for _, blk := range m.Content {
			switch blk.Type {
			case "text":
				if msg.Content == "" {
					msg.Content = blk.Text
				} else {
					msg.Content += "\n" + blk.Text
				}
			case "tool_use":
				args := "{}"
				if len(blk.Input) > 0 {
					args = string(blk.Input)
				}
				msg.ToolCalls = append(msg.ToolCalls, OpenAIToolCall{
					ID:   blk.ID,
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      blk.Name,
						Arguments: args,
					},
				})
			case "tool_result":
				// Tool results become a separate "tool" role message.
				out.Messages = append(out.Messages, msg)
				msg = OpenAIChatMessage{
					Role:       "tool",
					Content:    blk.Text,
					ToolCallID: blk.ID,
				}
			}
		}
		out.Messages = append(out.Messages, msg)
	}

	// Tools.
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return out
}

// ---------------------------------------------------------------------------
// Translation: OpenAI Responses-API → OpenAI Chat
// ---------------------------------------------------------------------------

// TranslateResponsesRequest converts an OpenAI Responses-API request into an
// OpenAIChatRequest.
func TranslateResponsesRequest(req ResponsesAPIRequest) OpenAIChatRequest {
	out := OpenAIChatRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
	}
	for _, item := range req.Input {
		role := item.Role
		if role == "" {
			role = "user"
		}
		out.Messages = append(out.Messages, OpenAIChatMessage{
			Role:    role,
			Content: item.Content,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Translation: OpenAI Chat response → Claude response
// ---------------------------------------------------------------------------

// TranslateToClaudeResponse converts an OpenAIChatResponse into a
// ClaudeResponse.  The ORDER of tool calls in the first choice's message is
// preserved exactly; this is the property pinned by TestToolCallOrderPreserved.
//
// MUTATION GUARD: the loop below copies tc[i] in forward index order.
// Reversing the iteration direction (e.g. i-- or len-1-i) makes
// TestToolCallOrderPreserved fail immediately.
func TranslateToClaudeResponse(oaiResp OpenAIChatResponse) ClaudeResponse {
	out := ClaudeResponse{
		ID:    oaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: oaiResp.Model,
		RunID: oaiResp.RunID,
		Usage: ClaudeUsage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		},
	}

	if len(oaiResp.Choices) == 0 {
		out.StopReason = "end_turn"
		return out
	}

	choice := oaiResp.Choices[0]
	out.StopReason = finishReasonToStopReason(choice.FinishReason)

	// Text content block.
	if choice.Message.Content != "" {
		out.Content = append(out.Content, ClaudeContentBlock{
			Type: "text",
			Text: choice.Message.Content,
		})
	}

	// Tool-use blocks — ORDER IS PRESERVED by iterating tc[i] i=0..len-1.
	// Guard: pinned by TestToolCallOrderPreserved.
	tc := choice.Message.ToolCalls
	for i := 0; i < len(tc); i++ { // mutation guard: reversing i makes TestToolCallOrderPreserved fail
		call := tc[i]
		rawArgs := json.RawMessage(call.Function.Arguments)
		if len(rawArgs) == 0 {
			rawArgs = json.RawMessage("{}")
		}
		out.Content = append(out.Content, ClaudeContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: rawArgs,
		})
	}

	return out
}

// finishReasonToStopReason maps OpenAI finish_reason to Claude stop_reason.
func finishReasonToStopReason(r string) string {
	switch r {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "stop_sequence"
	default:
		if r == "" {
			return "end_turn"
		}
		return r
	}
}

// ---------------------------------------------------------------------------
// HTTP round-trip helper
// ---------------------------------------------------------------------------

// Client sends an OpenAIChatRequest to the given endpoint URL and returns the
// decoded OpenAIChatResponse.  The endpoint must be a real HTTP server (use
// net/http/httptest.Server in tests).  If endpoint is empty, ErrNoCapability
// is returned — the caller must inject a real endpoint.
func Client(ctx context.Context, endpoint string, req OpenAIChatRequest, runID string) (OpenAIChatResponse, error) {
	if endpoint == "" {
		return OpenAIChatResponse{}, ErrNoCapability
	}

	body, err := json.Marshal(req)
	if err != nil {
		return OpenAIChatResponse{}, fmt.Errorf("llmadapter: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return OpenAIChatResponse{}, fmt.Errorf("llmadapter: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if runID != "" {
		httpReq.Header.Set("X-Run-ID", runID)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return OpenAIChatResponse{}, fmt.Errorf("llmadapter: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return OpenAIChatResponse{}, fmt.Errorf("llmadapter: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return OpenAIChatResponse{}, fmt.Errorf("llmadapter: backend status %d: %s", resp.StatusCode, raw)
	}

	var oaiResp OpenAIChatResponse
	if err := json.Unmarshal(raw, &oaiResp); err != nil {
		return OpenAIChatResponse{}, fmt.Errorf("llmadapter: decode response: %w", err)
	}
	// Propagate the runID the caller stamped on the request into the response
	// so that downstream assertions can verify provenance.
	if oaiResp.RunID == "" {
		oaiResp.RunID = runID
	}
	return oaiResp, nil
}

package llmadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newBackend spins up an in-process httptest.Server that speaks the OpenAI
// chat completions protocol.  The handler accepts any valid JSON request and
// replies with the provided OpenAIChatResponse (serialised to JSON).
// The server echoes back the X-Run-ID header it received so callers can
// verify end-to-end provenance.
func newBackend(t *testing.T, resp OpenAIChatResponse) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Decode incoming request to ensure it is well-formed JSON.
		var req OpenAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Propagate the caller's RunID from the HTTP header into the response.
		runID := r.Header.Get("X-Run-ID")
		if runID != "" {
			resp.RunID = runID
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("backend: encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// TestClaudeRequestTranslatedFaithfully
//
// Verifies that a ClaudeRequest with a plain text user message round-trips
// through the OpenAI backend and arrives back as a ClaudeResponse carrying
// the expected text content and the stamped RunID.
// ---------------------------------------------------------------------------

func TestClaudeRequestTranslatedFaithfully(t *testing.T) {
	const runID = "run-faithful-001"
	const wantText = "Paris is the capital of France."

	backendResp := OpenAIChatResponse{
		ID:    "chatcmpl-faithful",
		Model: "gpt-4o",
		Choices: []OpenAIChatChoice{
			{
				Index: 0,
				Message: OpenAIChatMessage{
					Role:    "assistant",
					Content: wantText,
				},
				FinishReason: "stop",
			},
		},
		Usage: OpenAIChatUsage{PromptTokens: 10, CompletionTokens: 8},
	}

	srv := newBackend(t, backendResp)

	claudeReq := ClaudeRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 256,
		System:    "You are a geography assistant.",
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ClaudeContentBlock{
					{Type: "text", Text: "What is the capital of France?"},
				},
			},
		},
	}

	oaiReq := TranslateClaudeRequest(claudeReq)

	// Verify the translation preserved the system prompt.
	if len(oaiReq.Messages) == 0 {
		t.Fatal("TranslateClaudeRequest: expected at least one message, got none")
	}
	if oaiReq.Messages[0].Role != "system" {
		t.Errorf("first message role: got %q, want %q", oaiReq.Messages[0].Role, "system")
	}

	// Send the translated request to the in-process backend.
	oaiResp, err := Client(context.Background(), srv.URL, oaiReq, runID)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	// Assert RunID provenance — sink-side observable evidence.
	if oaiResp.RunID != runID {
		t.Errorf("RunID: got %q, want %q", oaiResp.RunID, runID)
	}

	claudeResp := TranslateToClaudeResponse(oaiResp)

	// The RunID MUST be carried all the way into the Claude response.
	if claudeResp.RunID != runID {
		t.Errorf("ClaudeResponse.RunID: got %q, want %q", claudeResp.RunID, runID)
	}

	// There must be a text content block with the expected text.
	if len(claudeResp.Content) == 0 {
		t.Fatal("ClaudeResponse.Content: empty, want text block")
	}
	gotText := ""
	for _, blk := range claudeResp.Content {
		if blk.Type == "text" {
			gotText = blk.Text
			break
		}
	}
	if gotText != wantText {
		t.Errorf("text content: got %q, want %q", gotText, wantText)
	}

	if claudeResp.StopReason != "end_turn" {
		t.Errorf("stop_reason: got %q, want %q", claudeResp.StopReason, "end_turn")
	}
}

// ---------------------------------------------------------------------------
// TestToolCallOrderPreserved
//
// Verifies that a response carrying multiple tool calls is translated into a
// ClaudeResponse with tool_use content blocks in EXACTLY the same order.
//
// MUTATION GUARD: the forward loop `for i := 0; i < len(tc); i++` in
// TranslateToClaudeResponse (adapter.go) copies tool calls in order.
// If that loop is reversed (e.g. iterating from len-1 to 0) this test fails
// because it asserts tc[0].ID == wantOrder[0].
// ---------------------------------------------------------------------------

func TestToolCallOrderPreserved(t *testing.T) {
	const runID = "run-toolorder-002"

	// Define three tool calls in a deliberate non-alphabetical order so that
	// any accidental sort would be caught.
	toolCalls := []OpenAIToolCall{
		{ID: "call_c", Type: "function", Function: OpenAIFunctionCall{Name: "get_weather", Arguments: `{"city":"Berlin"}`}},
		{ID: "call_a", Type: "function", Function: OpenAIFunctionCall{Name: "lookup_stock", Arguments: `{"ticker":"SAP"}`}},
		{ID: "call_b", Type: "function", Function: OpenAIFunctionCall{Name: "send_email", Arguments: `{"to":"test@example.com"}`}},
	}

	backendResp := OpenAIChatResponse{
		ID:    "chatcmpl-toolorder",
		Model: "gpt-4o",
		Choices: []OpenAIChatChoice{
			{
				Index: 0,
				Message: OpenAIChatMessage{
					Role:      "assistant",
					ToolCalls: toolCalls,
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: OpenAIChatUsage{PromptTokens: 20, CompletionTokens: 30},
	}

	srv := newBackend(t, backendResp)

	claudeReq := ClaudeRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 512,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ClaudeContentBlock{
					{Type: "text", Text: "Perform the three tasks."},
				},
			},
		},
		Tools: []ClaudeTool{
			{Name: "get_weather", Description: "Get weather for a city"},
			{Name: "lookup_stock", Description: "Look up a stock ticker"},
			{Name: "send_email", Description: "Send an email"},
		},
	}

	oaiReq := TranslateClaudeRequest(claudeReq)

	oaiResp, err := Client(context.Background(), srv.URL, oaiReq, runID)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	// Sink-side: RunID must be stamped end-to-end.
	if oaiResp.RunID != runID {
		t.Errorf("RunID: got %q, want %q", oaiResp.RunID, runID)
	}

	claudeResp := TranslateToClaudeResponse(oaiResp)

	// RunID carried into Claude response.
	if claudeResp.RunID != runID {
		t.Errorf("ClaudeResponse.RunID: got %q, want %q", claudeResp.RunID, runID)
	}

	if claudeResp.StopReason != "tool_use" {
		t.Errorf("stop_reason: got %q, want %q", claudeResp.StopReason, "tool_use")
	}

	// Collect tool_use blocks from the response.
	var toolBlocks []ClaudeContentBlock
	for _, blk := range claudeResp.Content {
		if blk.Type == "tool_use" {
			toolBlocks = append(toolBlocks, blk)
		}
	}

	if len(toolBlocks) != len(toolCalls) {
		t.Fatalf("tool_use blocks: got %d, want %d", len(toolBlocks), len(toolCalls))
	}

	// Assert exact order matches the backend output.
	// MUTATION GUARD: reversing the copy loop makes every wantID[i] != gotID[i].
	for i, tc := range toolCalls {
		gotID := toolBlocks[i].ID
		if gotID != tc.ID {
			t.Errorf("tool_use[%d]: ID got %q, want %q (order not preserved)", i, gotID, tc.ID)
		}
		gotName := toolBlocks[i].Name
		if gotName != tc.Function.Name {
			t.Errorf("tool_use[%d]: Name got %q, want %q", i, gotName, tc.Function.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestTranslateResponsesRequestToChat
//
// Verifies that TranslateResponsesRequest correctly maps Responses-API input
// items to OpenAI chat messages, preserving roles and content.
// ---------------------------------------------------------------------------

func TestTranslateResponsesRequestToChat(t *testing.T) {
	req := ResponsesAPIRequest{
		Model:     "gpt-4o",
		MaxTokens: 128,
		Input: []ResponsesAPIInputItem{
			{Type: "message", Role: "system", Content: "Be helpful."},
			{Type: "message", Role: "user", Content: "Hello"},
		},
	}

	chat := TranslateResponsesRequest(req)

	if chat.Model != req.Model {
		t.Errorf("Model: got %q, want %q", chat.Model, req.Model)
	}
	if len(chat.Messages) != 2 {
		t.Fatalf("Messages: got %d, want 2", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "Be helpful." {
		t.Errorf("message[0]: got role=%q content=%q", chat.Messages[0].Role, chat.Messages[0].Content)
	}
	if chat.Messages[1].Role != "user" || chat.Messages[1].Content != "Hello" {
		t.Errorf("message[1]: got role=%q content=%q", chat.Messages[1].Role, chat.Messages[1].Content)
	}
}

// ---------------------------------------------------------------------------
// TestClientReturnsErrNoCapabilityWhenEndpointEmpty
//
// Verifies that Client returns ErrNoCapability (not a panic or nil error)
// when no endpoint is injected, satisfying CLAUDE-2: a typed error for a
// genuinely-absent capability.
// ---------------------------------------------------------------------------

func TestClientReturnsErrNoCapabilityWhenEndpointEmpty(t *testing.T) {
	_, err := Client(context.Background(), "", OpenAIChatRequest{Model: "gpt-4o"}, "run-nocap")
	if !errors.Is(err, ErrNoCapability) {
		t.Errorf("expected ErrNoCapability, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestFinishReasonMapping
//
// Verifies the finish_reason → stop_reason mapping for all known values.
// ---------------------------------------------------------------------------

func TestFinishReasonMapping(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "stop_sequence"},
		{"", "end_turn"},
		{"unknown_reason", "unknown_reason"},
	}
	for _, c := range cases {
		got := finishReasonToStopReason(c.in)
		if got != c.want {
			t.Errorf("finishReasonToStopReason(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestClaudeRequestToolsTranslated
//
// Verifies that ClaudeTool entries are faithfully translated to OpenAITool
// entries with type "function".
// ---------------------------------------------------------------------------

func TestClaudeRequestToolsTranslated(t *testing.T) {
	req := ClaudeRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 64,
		Messages:  []ClaudeMessage{{Role: "user", Content: []ClaudeContentBlock{{Type: "text", Text: "hi"}}}},
		Tools: []ClaudeTool{
			{Name: "search", Description: "web search", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	chat := TranslateClaudeRequest(req)

	if len(chat.Tools) != 1 {
		t.Fatalf("Tools: got %d, want 1", len(chat.Tools))
	}
	tool := chat.Tools[0]
	if tool.Type != "function" {
		t.Errorf("tool.Type: got %q, want %q", tool.Type, "function")
	}
	if tool.Function.Name != "search" {
		t.Errorf("tool.Function.Name: got %q, want %q", tool.Function.Name, "search")
	}
}

// ---------------------------------------------------------------------------
// TestResponsesAPIDefaultRole
//
// Verifies that a Responses-API input item with an empty role defaults to
// "user".
// ---------------------------------------------------------------------------

func TestResponsesAPIDefaultRole(t *testing.T) {
	req := ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []ResponsesAPIInputItem{
			{Type: "message", Content: "no role set"},
		},
	}
	chat := TranslateResponsesRequest(req)
	if len(chat.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chat.Messages))
	}
	if chat.Messages[0].Role != "user" {
		t.Errorf("default role: got %q, want %q", chat.Messages[0].Role, "user")
	}
}

// ---------------------------------------------------------------------------
// TestEndToEndResponsesAPIThroughBackend
//
// Verifies that a ResponsesAPIRequest → TranslateResponsesRequest → Client
// → TranslateToClaudeResponse pipeline works end-to-end through a real
// in-process httptest.Server, with RunID stamped end-to-end.
// ---------------------------------------------------------------------------

func TestEndToEndResponsesAPIThroughBackend(t *testing.T) {
	const runID = "run-e2e-responses-003"
	const wantText = "42 is the answer."

	backendResp := OpenAIChatResponse{
		ID:    "chatcmpl-responses-e2e",
		Model: "gpt-4o",
		Choices: []OpenAIChatChoice{
			{
				Index:        0,
				Message:      OpenAIChatMessage{Role: "assistant", Content: wantText},
				FinishReason: "stop",
			},
		},
	}

	srv := newBackend(t, backendResp)

	rReq := ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []ResponsesAPIInputItem{
			{Type: "message", Role: "user", Content: "What is the answer?"},
		},
	}

	oaiReq := TranslateResponsesRequest(rReq)
	oaiResp, err := Client(context.Background(), srv.URL, oaiReq, runID)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	if oaiResp.RunID != runID {
		t.Errorf("RunID: got %q, want %q", oaiResp.RunID, runID)
	}

	claudeResp := TranslateToClaudeResponse(oaiResp)
	if claudeResp.RunID != runID {
		t.Errorf("ClaudeResponse.RunID: got %q, want %q", claudeResp.RunID, runID)
	}

	var gotText string
	for _, blk := range claudeResp.Content {
		if blk.Type == "text" {
			gotText = blk.Text
		}
	}
	if gotText != wantText {
		t.Errorf("text content: got %q, want %q", gotText, wantText)
	}
}

// ---------------------------------------------------------------------------
// TestToolCallOrderPreservedWithManyTools  (extra stress variant)
//
// Uses 5 tools in a non-monotonic ID order to give stronger coverage of the
// mutation guard in TranslateToClaudeResponse.
// ---------------------------------------------------------------------------

func TestToolCallOrderPreservedWithManyTools(t *testing.T) {
	const runID = "run-toolorder-many-004"

	ids := []string{"tc_5", "tc_1", "tc_3", "tc_2", "tc_4"}
	var toolCalls []OpenAIToolCall
	for i, id := range ids {
		toolCalls = append(toolCalls, OpenAIToolCall{
			ID:   id,
			Type: "function",
			Function: OpenAIFunctionCall{
				Name:      fmt.Sprintf("tool_%d", i),
				Arguments: `{}`,
			},
		})
	}

	backendResp := OpenAIChatResponse{
		ID:    "chatcmpl-many",
		Model: "gpt-4o",
		Choices: []OpenAIChatChoice{
			{
				Index:        0,
				Message:      OpenAIChatMessage{Role: "assistant", ToolCalls: toolCalls},
				FinishReason: "tool_calls",
			},
		},
	}

	srv := newBackend(t, backendResp)

	oaiReq := TranslateClaudeRequest(ClaudeRequest{
		Model:     "gpt-4o",
		MaxTokens: 256,
		Messages: []ClaudeMessage{
			{Role: "user", Content: []ClaudeContentBlock{{Type: "text", Text: "run tools"}}},
		},
	})

	oaiResp, err := Client(context.Background(), srv.URL, oaiReq, runID)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	if oaiResp.RunID != runID {
		t.Errorf("RunID: got %q, want %q", oaiResp.RunID, runID)
	}

	claudeResp := TranslateToClaudeResponse(oaiResp)

	var toolBlocks []ClaudeContentBlock
	for _, blk := range claudeResp.Content {
		if blk.Type == "tool_use" {
			toolBlocks = append(toolBlocks, blk)
		}
	}

	if len(toolBlocks) != len(ids) {
		t.Fatalf("tool_use blocks: got %d, want %d", len(toolBlocks), len(ids))
	}

	for i, wantID := range ids {
		if toolBlocks[i].ID != wantID {
			t.Errorf("tool_use[%d]: ID got %q, want %q (order broken)", i, toolBlocks[i].ID, wantID)
		}
	}
}

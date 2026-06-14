package llmadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ===========================================================================
// Adversarial, mutation-verified tests pinning the translation CONTRACT of
// pkg/llmadapter.  Centerpieces:
//   - field fidelity (every field lands in the right target field)
//   - order preservation (messages, tool calls, content blocks)
// Each test uses DISTINCT values so a field-swap is detectable, and asserts
// against a hand-built expectation rather than merely "didn't error".
// ===========================================================================

// ---------------------------------------------------------------------------
// CENTERPIECE 1 — Claude→Chat request field fidelity.
//
// A fully-populated ClaudeRequest with distinct values in every field, two
// messages, and a tool. We assert each field landed in the correct target
// field of OpenAIChatRequest against a hand-built expectation.
// ---------------------------------------------------------------------------

func TestAdversarial_ClaudeRequestFieldFidelity(t *testing.T) {
	req := ClaudeRequest{
		Model:     "claude-model-XYZ",
		MaxTokens: 4321, // distinct, non-default
		System:    "SYS-PROMPT-DISTINCT",
		Messages: []ClaudeMessage{
			{Role: "user", Content: []ClaudeContentBlock{{Type: "text", Text: "USER-TEXT-1"}}},
			{Role: "assistant", Content: []ClaudeContentBlock{{Type: "text", Text: "ASSISTANT-TEXT-2"}}},
		},
		Tools: []ClaudeTool{
			{
				Name:        "TOOL-NAME",
				Description: "TOOL-DESC",
				InputSchema: json.RawMessage(`{"k":"SCHEMA-VAL"}`),
			},
		},
	}

	got := TranslateClaudeRequest(req)

	// Scalar parameter mapping.
	if got.Model != "claude-model-XYZ" {
		t.Errorf("Model dropped/mis-mapped: got %q", got.Model)
	}
	if got.MaxTokens != 4321 {
		t.Errorf("MaxTokens dropped/mis-mapped: got %d, want 4321", got.MaxTokens)
	}

	// System prompt MUST be the first message with role "system" and exact text.
	if len(got.Messages) != 3 { // system + 2 turns
		t.Fatalf("message count: got %d, want 3 (system + 2 turns); messages=%+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "SYS-PROMPT-DISTINCT" {
		t.Errorf("system message corrupted: role=%q content=%q", got.Messages[0].Role, got.Messages[0].Content)
	}

	// Message ordering + content fidelity (user before assistant; texts not swapped).
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "USER-TEXT-1" {
		t.Errorf("message[1] corrupted: role=%q content=%q (want user/USER-TEXT-1)", got.Messages[1].Role, got.Messages[1].Content)
	}
	if got.Messages[2].Role != "assistant" || got.Messages[2].Content != "ASSISTANT-TEXT-2" {
		t.Errorf("message[2] corrupted: role=%q content=%q (want assistant/ASSISTANT-TEXT-2)", got.Messages[2].Role, got.Messages[2].Content)
	}

	// Tool fidelity: name, description, schema all preserved, type=="function".
	if len(got.Tools) != 1 {
		t.Fatalf("tool count: got %d, want 1", len(got.Tools))
	}
	tl := got.Tools[0]
	if tl.Type != "function" {
		t.Errorf("tool type: got %q, want function", tl.Type)
	}
	if tl.Function.Name != "TOOL-NAME" {
		t.Errorf("tool name dropped/swapped: got %q", tl.Function.Name)
	}
	if tl.Function.Description != "TOOL-DESC" {
		t.Errorf("tool description dropped/swapped: got %q", tl.Function.Description)
	}
	if string(tl.Function.Parameters) != `{"k":"SCHEMA-VAL"}` {
		t.Errorf("tool input_schema dropped/corrupted: got %q", string(tl.Function.Parameters))
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 1b — multi-content-block fidelity inside ONE message.
//
// A single assistant message carrying text + two tool_use blocks in a
// deliberate order. Probes: text not lost, tool_use blocks become ToolCalls
// in the SAME order, each tool call's id/name/arguments mapped to the right
// field (no field swap), arguments preserved verbatim.
// ---------------------------------------------------------------------------

func TestAdversarial_ClaudeMultiBlockMessageFidelity(t *testing.T) {
	req := ClaudeRequest{
		Model:     "m",
		MaxTokens: 1,
		Messages: []ClaudeMessage{
			{
				Role: "assistant",
				Content: []ClaudeContentBlock{
					{Type: "text", Text: "PREAMBLE"},
					{Type: "tool_use", ID: "id_FIRST", Name: "name_FIRST", Input: json.RawMessage(`{"a":1}`)},
					{Type: "tool_use", ID: "id_SECOND", Name: "name_SECOND", Input: json.RawMessage(`{"b":2}`)},
				},
			},
		},
	}

	got := TranslateClaudeRequest(req)
	if len(got.Messages) != 1 {
		t.Fatalf("message count: got %d, want 1; %+v", len(got.Messages), got.Messages)
	}
	m := got.Messages[0]
	if m.Role != "assistant" {
		t.Errorf("role: got %q, want assistant", m.Role)
	}
	if m.Content != "PREAMBLE" {
		t.Errorf("text block lost/corrupted: got %q, want PREAMBLE", m.Content)
	}
	if len(m.ToolCalls) != 2 {
		t.Fatalf("tool_calls: got %d, want 2", len(m.ToolCalls))
	}
	// Order + per-field fidelity. Distinct values catch id<->name swaps.
	if m.ToolCalls[0].ID != "id_FIRST" || m.ToolCalls[0].Function.Name != "name_FIRST" {
		t.Errorf("tool_call[0] field swap/reorder: id=%q name=%q", m.ToolCalls[0].ID, m.ToolCalls[0].Function.Name)
	}
	if string(m.ToolCalls[0].Function.Arguments) != `{"a":1}` {
		t.Errorf("tool_call[0] arguments corrupted: got %q", m.ToolCalls[0].Function.Arguments)
	}
	if m.ToolCalls[1].ID != "id_SECOND" || m.ToolCalls[1].Function.Name != "name_SECOND" {
		t.Errorf("tool_call[1] field swap/reorder: id=%q name=%q", m.ToolCalls[1].ID, m.ToolCalls[1].Function.Name)
	}
	if string(m.ToolCalls[1].Function.Arguments) != `{"b":2}` {
		t.Errorf("tool_call[1] arguments corrupted: got %q", m.ToolCalls[1].Function.Arguments)
	}
	if m.ToolCalls[0].Type != "function" || m.ToolCalls[1].Type != "function" {
		t.Errorf("tool_call type not 'function': %q %q", m.ToolCalls[0].Type, m.ToolCalls[1].Type)
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2 — response tool-call ORDER preservation with full field map.
//
// A map-based intermediate would scramble this. Distinct, non-sorted IDs with
// matching distinct names/arguments; we assert order AND that each field
// landed in the right Claude content-block field.
// ---------------------------------------------------------------------------

func TestAdversarial_ResponseToolOrderAndFieldFidelity(t *testing.T) {
	calls := []OpenAIToolCall{
		{ID: "Z_first", Type: "function", Function: OpenAIFunctionCall{Name: "alpha_fn", Arguments: `{"q":"Z"}`}},
		{ID: "A_second", Type: "function", Function: OpenAIFunctionCall{Name: "beta_fn", Arguments: `{"q":"A"}`}},
		{ID: "M_third", Type: "function", Function: OpenAIFunctionCall{Name: "gamma_fn", Arguments: `{"q":"M"}`}},
	}
	resp := OpenAIChatResponse{
		ID:    "resp-1",
		Model: "gpt-x",
		Choices: []OpenAIChatChoice{{
			Index:        0,
			Message:      OpenAIChatMessage{Role: "assistant", Content: "LEAD-TEXT", ToolCalls: calls},
			FinishReason: "tool_calls",
		}},
		Usage: OpenAIChatUsage{PromptTokens: 7, CompletionTokens: 11},
	}

	out := TranslateToClaudeResponse(resp)

	// Response-level field fidelity.
	if out.ID != "resp-1" {
		t.Errorf("response ID dropped: got %q", out.ID)
	}
	if out.Model != "gpt-x" {
		t.Errorf("response Model dropped: got %q", out.Model)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Errorf("type/role: got type=%q role=%q", out.Type, out.Role)
	}
	if out.StopReason != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", out.StopReason)
	}
	if out.Usage.InputTokens != 7 || out.Usage.OutputTokens != 11 {
		t.Errorf("usage mis-mapped: in=%d out=%d (want 7/11) — note prompt->input, completion->output", out.Usage.InputTokens, out.Usage.OutputTokens)
	}

	// Content blocks: first the text block, then tool_use blocks IN ORDER.
	if len(out.Content) != 4 { // 1 text + 3 tool_use
		t.Fatalf("content blocks: got %d, want 4; %+v", len(out.Content), out.Content)
	}
	if out.Content[0].Type != "text" || out.Content[0].Text != "LEAD-TEXT" {
		t.Errorf("leading text block corrupted: %+v", out.Content[0])
	}

	wantIDs := []string{"Z_first", "A_second", "M_third"}
	wantNames := []string{"alpha_fn", "beta_fn", "gamma_fn"}
	wantArgs := []string{`{"q":"Z"}`, `{"q":"A"}`, `{"q":"M"}`}
	for i := 0; i < 3; i++ {
		blk := out.Content[i+1]
		if blk.Type != "tool_use" {
			t.Errorf("content[%d] type: got %q, want tool_use", i+1, blk.Type)
		}
		if blk.ID != wantIDs[i] {
			t.Errorf("tool_use[%d] ID: got %q, want %q (ORDER SCRAMBLED or field swap)", i, blk.ID, wantIDs[i])
		}
		if blk.Name != wantNames[i] {
			t.Errorf("tool_use[%d] Name: got %q, want %q", i, blk.Name, wantNames[i])
		}
		if string(blk.Input) != wantArgs[i] {
			t.Errorf("tool_use[%d] Input: got %q, want %q", i, string(blk.Input), wantArgs[i])
		}
	}
}

// ---------------------------------------------------------------------------
// PARAMETER MAPPING — Responses-API → Chat: max_output_tokens lands in
// MaxTokens, model preserved, roles/content preserved IN ORDER, empty
// max_output_tokens stays zero (omitted optional, not corrupted).
// ---------------------------------------------------------------------------

func TestAdversarial_ResponsesParamAndOrderMapping(t *testing.T) {
	req := ResponsesAPIRequest{
		Model:     "resp-model",
		MaxTokens: 999, // max_output_tokens
		Input: []ResponsesAPIInputItem{
			{Type: "message", Role: "system", Content: "S"},
			{Type: "message", Role: "user", Content: "U1"},
			{Type: "message", Role: "assistant", Content: "A1"},
			{Type: "message", Role: "user", Content: "U2"},
		},
	}
	got := TranslateResponsesRequest(req)

	if got.Model != "resp-model" {
		t.Errorf("model dropped: %q", got.Model)
	}
	if got.MaxTokens != 999 {
		t.Errorf("max_output_tokens not mapped to MaxTokens: got %d, want 999", got.MaxTokens)
	}
	wantRoles := []string{"system", "user", "assistant", "user"}
	wantContent := []string{"S", "U1", "A1", "U2"}
	if len(got.Messages) != 4 {
		t.Fatalf("messages: got %d, want 4", len(got.Messages))
	}
	for i := range wantRoles {
		if got.Messages[i].Role != wantRoles[i] || got.Messages[i].Content != wantContent[i] {
			t.Errorf("message[%d] reordered/corrupted: got role=%q content=%q want role=%q content=%q",
				i, got.Messages[i].Role, got.Messages[i].Content, wantRoles[i], wantContent[i])
		}
	}

	// Absent optional: max_output_tokens omitted -> MaxTokens zero, not garbage.
	none := TranslateResponsesRequest(ResponsesAPIRequest{Model: "m", Input: []ResponsesAPIInputItem{{Role: "user", Content: "x"}}})
	if none.MaxTokens != 0 {
		t.Errorf("absent max_output_tokens should map to 0, got %d", none.MaxTokens)
	}
}

// ---------------------------------------------------------------------------
// CAPABILITY GUARD — empty endpoint returns ErrNoCapability; a real endpoint
// proceeds and returns a decoded response.
// ---------------------------------------------------------------------------

func TestAdversarial_CapabilityGuard(t *testing.T) {
	// Empty endpoint -> typed ErrNoCapability, no panic.
	_, err := Client(context.Background(), "", OpenAIChatRequest{Model: "m"}, "run-x")
	if !errors.Is(err, ErrNoCapability) {
		t.Fatalf("empty endpoint: got err=%v, want ErrNoCapability", err)
	}

	// Valid endpoint -> proceeds, decodes response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(OpenAIChatResponse{ID: "ok", Model: "m"})
	}))
	defer srv.Close()

	resp, err := Client(context.Background(), srv.URL, OpenAIChatRequest{Model: "m"}, "run-y")
	if err != nil {
		t.Fatalf("valid endpoint: unexpected err %v", err)
	}
	if resp.ID != "ok" {
		t.Errorf("decoded response ID: got %q, want ok", resp.ID)
	}
	if resp.RunID != "run-y" {
		t.Errorf("RunID stamp when backend omits it: got %q, want run-y", resp.RunID)
	}
}

// ---------------------------------------------------------------------------
// EDGES — empty messages, a message with no content, nil tool list, empty
// arguments default to "{}" / "{}" RawMessage. No panics.
// ---------------------------------------------------------------------------

func TestAdversarial_Edges(t *testing.T) {
	// Empty messages + nil tools: must not panic, produces empty translation.
	got := TranslateClaudeRequest(ClaudeRequest{Model: "m", MaxTokens: 5})
	if len(got.Messages) != 0 {
		t.Errorf("empty request should yield 0 messages, got %d", len(got.Messages))
	}
	if got.Tools != nil {
		t.Errorf("nil tools should stay nil, got %+v", got.Tools)
	}

	// Message with a content block carrying no text/tool: still emits one
	// message with the role (no panic, role preserved).
	got2 := TranslateClaudeRequest(ClaudeRequest{
		Model: "m",
		Messages: []ClaudeMessage{
			{Role: "user", Content: nil},
		},
	})
	if len(got2.Messages) != 1 || got2.Messages[0].Role != "user" {
		t.Errorf("no-content message: got %+v", got2.Messages)
	}

	// Response with empty tool-call arguments -> Input defaults to "{}".
	resp := OpenAIChatResponse{
		Choices: []OpenAIChatChoice{{
			Message: OpenAIChatMessage{
				Role:      "assistant",
				ToolCalls: []OpenAIToolCall{{ID: "c1", Type: "function", Function: OpenAIFunctionCall{Name: "fn", Arguments: ""}}},
			},
			FinishReason: "tool_calls",
		}},
	}
	out := TranslateToClaudeResponse(resp)
	var toolBlk *ClaudeContentBlock
	for i := range out.Content {
		if out.Content[i].Type == "tool_use" {
			toolBlk = &out.Content[i]
		}
	}
	if toolBlk == nil {
		t.Fatal("expected a tool_use block")
	}
	if string(toolBlk.Input) != "{}" {
		t.Errorf("empty arguments should default to {}: got %q", string(toolBlk.Input))
	}

	// Empty choices -> end_turn, no panic, no content.
	empty := TranslateToClaudeResponse(OpenAIChatResponse{ID: "e", Model: "m"})
	if empty.StopReason != "end_turn" {
		t.Errorf("empty choices stop_reason: got %q, want end_turn", empty.StopReason)
	}
	if len(empty.Content) != 0 {
		t.Errorf("empty choices content: got %d blocks, want 0", len(empty.Content))
	}
}

// ---------------------------------------------------------------------------
// tool_result block translation: becomes a separate "tool" role message
// carrying the result text and tool_call_id. Pins current behavior so a
// regression in the flush logic is caught.
// ---------------------------------------------------------------------------

func TestAdversarial_ToolResultBecomesToolMessage(t *testing.T) {
	req := ClaudeRequest{
		Model: "m",
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ClaudeContentBlock{
					{Type: "tool_result", ID: "call_42", Text: "RESULT-TEXT"},
				},
			},
		},
	}
	got := TranslateClaudeRequest(req)

	// Find the tool-role message and verify its fields.
	var toolMsg *OpenAIChatMessage
	for i := range got.Messages {
		if got.Messages[i].Role == "tool" {
			toolMsg = &got.Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatalf("expected a tool-role message; got %+v", got.Messages)
	}
	if toolMsg.Content != "RESULT-TEXT" {
		t.Errorf("tool result text dropped/corrupted: got %q", toolMsg.Content)
	}
	if toolMsg.ToolCallID != "call_42" {
		t.Errorf("tool_call_id dropped/corrupted: got %q, want call_42", toolMsg.ToolCallID)
	}
}

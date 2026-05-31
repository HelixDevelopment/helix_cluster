package backends

import (
	"bytes"
	"strings"
	"testing"
)

// --- 6. tmux control-mode (-CC) parser tests ---
//
// Each test carries a "MUTATION:" note naming the one-line code change that
// makes the field-exact assertions fail. These prove REAL decoding/routing
// behavior, not mere absence of panic.

// eventTypes extracts the ordered Type sequence for sequence assertions.
func eventTypes(evs []ControlEvent) []ControlEventType {
	out := make([]ControlEventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func typesEqual(a []ControlEventType, b ...ControlEventType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestControlParser_OutputDecodesOctalPayload proves %output payload is
// octal-decoded and routed to the correct pane id.
//
// MUTATION: in decodeControl, replace the octal-decode branch with
// `out = append(out, c)` (drop escape handling) -> decoded bytes mismatch.
// MUTATION: in parseOutput, set PaneID to "" -> pane-id assert fails.
func TestControlParser_OutputDecodesOctalPayload(t *testing.T) {
	// \015\012 == CR LF; \033 == ESC. "OK" printable around them.
	transcript := "%output %3 OK\\015\\012\\033bye\n"
	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(evs), evs)
	}
	e := evs[0]
	if e.Type != EventOutput {
		t.Fatalf("expected EventOutput, got %s", e.Type)
	}
	if e.PaneID != "%3" {
		t.Fatalf("expected pane id %%3, got %q", e.PaneID)
	}
	want := []byte{'O', 'K', 0x0d, 0x0a, 0x1b, 'b', 'y', 'e'}
	if !bytes.Equal(e.Data, want) {
		t.Fatalf("decoded payload mismatch:\n want %v\n got  %v", want, e.Data)
	}
}

// TestControlParser_OutputRoutingDistinctPanes proves payloads are NOT
// cross-routed between panes.
//
// MUTATION: in parseOutput, hard-code PaneID = "%0" -> second pane assert
// fails (got %0, want %7).
func TestControlParser_OutputRoutingDistinctPanes(t *testing.T) {
	transcript := "%output %1 alpha\n%output %7 omega\n"
	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}
	if evs[0].PaneID != "%1" || string(evs[0].Data) != "alpha" {
		t.Fatalf("event0 wrong: pane=%q data=%q", evs[0].PaneID, evs[0].Data)
	}
	if evs[1].PaneID != "%7" || string(evs[1].Data) != "omega" {
		t.Fatalf("event1 wrong: pane=%q data=%q", evs[1].PaneID, evs[1].Data)
	}
}

// TestControlParser_BeginEndFraming proves a full %begin..%end block emits
// EventBegin, EventEnd, then a synthesized EventCommandResponse carrying the
// decoded body, with command-id and timestamp preserved.
//
// MUTATION: in closeBlock, return only []ControlEvent{frame} (drop resp) ->
// the EventCommandResponse + body assertions fail.
// MUTATION: in appendBody, skip appending body -> resp.Data mismatch.
func TestControlParser_BeginEndFraming(t *testing.T) {
	transcript := strings.Join([]string{
		"%begin 1733000000 17 0",
		"line-one",
		"line-two",
		"%end 1733000000 17 0",
	}, "\n") + "\n"

	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := eventTypes(evs); !typesEqual(got, EventBegin, EventEnd, EventCommandResponse) {
		t.Fatalf("event type sequence = %v, want [begin end command-response]", got)
	}
	begin := evs[0]
	if begin.CommandID != "17" || begin.Timestamp != "1733000000" || begin.Flags != "0" {
		t.Fatalf("begin fields wrong: %+v", begin)
	}
	end := evs[1]
	if end.CommandID != "17" || end.Timestamp != "1733000000" {
		t.Fatalf("end fields wrong: %+v", end)
	}
	resp := evs[2]
	if resp.Errored {
		t.Fatalf("expected non-errored response")
	}
	if resp.CommandID != "17" {
		t.Fatalf("response command id = %q, want 17", resp.CommandID)
	}
	if string(resp.Data) != "line-one\nline-two" {
		t.Fatalf("response body = %q, want %q", resp.Data, "line-one\nline-two")
	}
}

// TestControlParser_BeginErrorFraming proves %error closes a block and the
// synthesized response is flagged Errored with the body preserved.
//
// MUTATION: in closeBlock, hard-code errored=false (resp.Errored) -> Errored
// assert fails.
func TestControlParser_BeginErrorFraming(t *testing.T) {
	transcript := strings.Join([]string{
		"%begin 1733000100 42 0",
		"no such window: bogus",
		"%error 1733000100 42 0",
	}, "\n") + "\n"

	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := eventTypes(evs); !typesEqual(got, EventBegin, EventError, EventCommandResponse) {
		t.Fatalf("event type sequence = %v, want [begin error command-response]", got)
	}
	resp := evs[2]
	if !resp.Errored {
		t.Fatalf("expected Errored response on %%error close")
	}
	if string(resp.Data) != "no such window: bogus" {
		t.Fatalf("error body = %q", resp.Data)
	}
}

// TestControlParser_OutputInsideBlockIsBody proves that a line that looks like
// %output but appears INSIDE a %begin block is treated as response body, not
// re-routed as a pane event. This is the core "drop %end framing fails" gate:
// without block tracking, the inner line would mis-route.
//
// MUTATION: delete the `if p.inBlock { ... }` fast-path in ParseLine (so block
// body is parsed as notifications) -> sequence becomes [begin output end ...]
// instead of [begin end command-response], failing the assert. (Note: tmux
// escapes literal %-lines inside blocks; this models a literal body line.)
func TestControlParser_OutputInsideBlockIsBody(t *testing.T) {
	transcript := strings.Join([]string{
		"%begin 1 5 0",
		"some normal output text",
		"%end 1 5 0",
	}, "\n") + "\n"

	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := eventTypes(evs); !typesEqual(got, EventBegin, EventEnd, EventCommandResponse) {
		t.Fatalf("sequence = %v, want [begin end command-response]", got)
	}
	if string(evs[2].Data) != "some normal output text" {
		t.Fatalf("body = %q", evs[2].Data)
	}
}

// TestControlParser_FullMixedTranscript proves an interleaved real-world
// transcript yields the EXACT ordered sequence of event types and fields.
//
// MUTATION: in ParseLine, route "%window-add" to parseWindowID with
// EventWindowClose -> window-add type assert fails.
func TestControlParser_FullMixedTranscript(t *testing.T) {
	transcript := strings.Join([]string{
		"%session-changed $0 main",
		"%window-add @1",
		"%output %0 hello\\040world",
		"%layout-change @1 bb62,80x24,0,0,0",
		"%begin 1733000200 1 0",
		"resp-body",
		"%end 1733000200 1 0",
		"%window-close @1",
		"%exit shutting down",
	}, "\n") + "\n"

	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	wantSeq := []ControlEventType{
		EventSessionChanged,
		EventWindowAdd,
		EventOutput,
		EventLayoutChange,
		EventBegin,
		EventEnd,
		EventCommandResponse,
		EventWindowClose,
		EventExit,
	}
	if got := eventTypes(evs); !typesEqual(got, wantSeq...) {
		t.Fatalf("sequence = %v\n   want = %v", got, wantSeq)
	}

	// Field-exact spot checks across the sequence.
	if evs[0].SessionID != "$0" || evs[0].Name != "main" {
		t.Fatalf("session-changed fields: %+v", evs[0])
	}
	if evs[1].WindowID != "@1" {
		t.Fatalf("window-add id = %q", evs[1].WindowID)
	}
	if evs[2].PaneID != "%0" || string(evs[2].Data) != "hello world" {
		t.Fatalf("output: pane=%q data=%q (want %%0 / 'hello world')", evs[2].PaneID, evs[2].Data)
	}
	if evs[3].WindowID != "@1" || evs[3].Layout != "bb62,80x24,0,0,0" {
		t.Fatalf("layout-change: %+v", evs[3])
	}
	if string(evs[6].Data) != "resp-body" {
		t.Fatalf("command-response body = %q", evs[6].Data)
	}
	if evs[7].WindowID != "@1" {
		t.Fatalf("window-close id = %q", evs[7].WindowID)
	}
	if evs[8].Reason != "shutting down" {
		t.Fatalf("exit reason = %q", evs[8].Reason)
	}
}

// TestControlParser_UnterminatedBegin proves a %begin without %end is handled:
// events parsed so far (including EventBegin) are returned, plus a clear error,
// and NO EventCommandResponse is synthesized for the dangling block.
//
// MUTATION: in Parse, drop the `if p.inBlock { return ..., error }` check ->
// no error returned, test's "expected error" assert fails.
func TestControlParser_UnterminatedBegin(t *testing.T) {
	transcript := strings.Join([]string{
		"%output %2 before",
		"%begin 1733000300 9 0",
		"dangling body",
	}, "\n") + "\n"

	p := NewControlParser()
	evs, err := p.Parse(strings.NewReader(transcript))
	if err == nil {
		t.Fatalf("expected error for unterminated %%begin block")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("error should mention unterminated block, got: %v", err)
	}
	// The %output and the %begin frame must still be present; no synthesized
	// command-response for the open block.
	if got := eventTypes(evs); !typesEqual(got, EventOutput, EventBegin) {
		t.Fatalf("sequence = %v, want [output begin]", got)
	}
	for _, e := range evs {
		if e.Type == EventCommandResponse {
			t.Fatalf("must NOT synthesize command-response for dangling block")
		}
	}
}

// TestControlParser_MalformedOutputMissingPane proves a malformed %output
// (no %<pane>) yields a clear error rather than a silent/empty event.
//
// MUTATION: in parseOutput, delete the prefix/length guard and always return
// the event -> no error, test's "expected error" assert fails.
func TestControlParser_MalformedOutputMissingPane(t *testing.T) {
	p := NewControlParser()
	_, err := p.ParseLine("%output notapane data")
	if err == nil {
		t.Fatalf("expected error for %%output without %%<pane> id")
	}
	if !strings.Contains(err.Error(), "pane") {
		t.Fatalf("error should mention pane, got: %v", err)
	}
}

// TestControlParser_MalformedBeginFieldCount proves %begin with the wrong
// field count is a clear error.
//
// MUTATION: in threeFields, change `len(f) != 3` to `len(f) < 1` -> the
// 2-field line is accepted, test's "expected error" assert fails.
func TestControlParser_MalformedBeginFieldCount(t *testing.T) {
	p := NewControlParser()
	_, err := p.ParseLine("%begin 123 7") // missing flags field
	if err == nil {
		t.Fatalf("expected error for %%begin with 2 fields")
	}
	if !strings.Contains(err.Error(), "3 fields") {
		t.Fatalf("error should mention field count, got: %v", err)
	}
}

// TestControlParser_StrayEndIsError proves an %end with no open %begin is a
// protocol error (framing cannot be silently dropped).
//
// MUTATION: in ParseLine's switch, route "%end"/"%error" to return nil,nil ->
// no error, assert fails.
func TestControlParser_StrayEndIsError(t *testing.T) {
	p := NewControlParser()
	_, err := p.ParseLine("%end 1 1 0")
	if err == nil {
		t.Fatalf("expected error for stray %%end")
	}
	if !strings.Contains(err.Error(), "no matching") {
		t.Fatalf("error should mention missing %%begin, got: %v", err)
	}
}

// TestControlParser_NonNotificationOutsideBlockIsError proves a bare
// (non-%) line outside any block is a protocol violation.
//
// MUTATION: in ParseLine, change the non-'%' branch to return nil,nil ->
// no error, assert fails.
func TestControlParser_NonNotificationOutsideBlockIsError(t *testing.T) {
	p := NewControlParser()
	_, err := p.ParseLine("just some text")
	if err == nil {
		t.Fatalf("expected error for non-notification line outside block")
	}
}

// TestControlParser_EmptyOutputPayload proves an %output with empty payload
// decodes to a nil/empty byte slice but still carries the correct pane id.
//
// MUTATION: in decodeControl, return []byte{0} for empty input -> non-empty
// data assert fails.
func TestControlParser_EmptyOutputPayload(t *testing.T) {
	p := NewControlParser()
	evs, err := p.ParseLine("%output %5 ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(evs) != 1 || evs[0].PaneID != "%5" {
		t.Fatalf("unexpected: %+v", evs)
	}
	if len(evs[0].Data) != 0 {
		t.Fatalf("expected empty payload, got %v", evs[0].Data)
	}
}

// TestControlParser_BackslashNotOctalPassthrough proves a backslash that is
// not a valid \ooo escape is passed through literally (no data loss).
//
// MUTATION: in decodeControl, drop the literal-backslash fallback (skip the
// byte) -> the backslash is lost, byte-exact assert fails.
func TestControlParser_BackslashNotOctalPassthrough(t *testing.T) {
	// "\9" is not octal (9 invalid); must survive as backslash + '9'.
	evs, err := NewControlParser().ParseLine("%output %0 a\\9b")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []byte{'a', '\\', '9', 'b'}
	if !bytes.Equal(evs[0].Data, want) {
		t.Fatalf("passthrough mismatch: want %v got %v", want, evs[0].Data)
	}
}

// TestControlParser_StreamingViaParseLine proves the incremental ParseLine API
// produces the same block semantics as Parse, with body accumulated across
// multiple calls.
//
// MUTATION: in appendBody, reset p.block.lineCount each call (so '\n' joins
// vanish) -> two-line body joins wrong, assert fails.
func TestControlParser_StreamingViaParseLine(t *testing.T) {
	p := NewControlParser()
	var all []ControlEvent
	for _, line := range []string{
		"%begin 7 3 0",
		"aaa",
		"bbb",
		"%end 7 3 0",
	} {
		evs, err := p.ParseLine(line)
		if err != nil {
			t.Fatalf("parseline %q: %v", line, err)
		}
		all = append(all, evs...)
	}
	if got := eventTypes(all); !typesEqual(got, EventBegin, EventEnd, EventCommandResponse) {
		t.Fatalf("sequence = %v", got)
	}
	if string(all[2].Data) != "aaa\nbbb" {
		t.Fatalf("streamed body = %q, want %q", all[2].Data, "aaa\nbbb")
	}
}

// TestControlParser_MismatchedEndID proves an %end whose command id differs
// from the opening %begin surfaces an error while still returning the frame
// and response.
//
// MUTATION: in closeBlock, delete the `if cmd != beginCmd` mismatch check ->
// no error, assert fails.
func TestControlParser_MismatchedEndID(t *testing.T) {
	p := NewControlParser()
	if _, err := p.ParseLine("%begin 1 4 0"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	evs, err := p.ParseLine("%end 1 99 0")
	if err == nil {
		t.Fatalf("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error should report mismatch, got: %v", err)
	}
	// Frame + response are still emitted for the caller.
	if got := eventTypes(evs); !typesEqual(got, EventEnd, EventCommandResponse) {
		t.Fatalf("sequence = %v, want [end command-response]", got)
	}
}

// TestControlEventType_String proves the String() rendering used by assertions
// and logs is correct for each type (so a wrong const ordering is caught).
//
// MUTATION: swap two return strings in String() (e.g. "output"<->"begin") ->
// assert fails.
func TestControlEventType_String(t *testing.T) {
	cases := map[ControlEventType]string{
		EventBegin:           "begin",
		EventEnd:             "end",
		EventError:           "error",
		EventCommandResponse: "command-response",
		EventOutput:          "output",
		EventWindowAdd:       "window-add",
		EventWindowClose:     "window-close",
		EventSessionChanged:  "session-changed",
		EventLayoutChange:    "layout-change",
		EventExit:            "exit",
		EventUnknown:         "unknown",
	}
	for typ, want := range cases {
		if got := typ.String(); got != want {
			t.Fatalf("String(%d) = %q, want %q", typ, got, want)
		}
	}
}

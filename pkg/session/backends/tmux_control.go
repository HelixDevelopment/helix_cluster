package backends

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ControlEventType enumerates the kinds of notifications a tmux control-mode
// (-CC) server emits on its line-oriented protocol.
type ControlEventType int

const (
	// EventUnknown is the zero value and indicates an unrecognized line.
	EventUnknown ControlEventType = iota
	// EventBegin marks the start of a command-response block (%begin).
	EventBegin
	// EventEnd marks the successful end of a command-response block (%end).
	EventEnd
	// EventError marks the failed end of a command-response block (%error).
	EventError
	// EventCommandResponse is a synthesized event delivered once a
	// %begin..%end (or %begin..%error) block has been fully read. Its
	// Data field holds the decoded body bytes of the response.
	EventCommandResponse
	// EventOutput carries pane output (%output %<pane> <data>).
	EventOutput
	// EventWindowAdd signals a new window (%window-add @<id>).
	EventWindowAdd
	// EventWindowClose signals a closed window (%window-close @<id>).
	EventWindowClose
	// EventSessionChanged signals the active session changed (%session-changed).
	EventSessionChanged
	// EventLayoutChange signals a window layout change (%layout-change).
	EventLayoutChange
	// EventExit signals the control-mode server is exiting (%exit).
	EventExit
)

// String renders the event type for diagnostics and assertions.
func (t ControlEventType) String() string {
	switch t {
	case EventBegin:
		return "begin"
	case EventEnd:
		return "end"
	case EventError:
		return "error"
	case EventCommandResponse:
		return "command-response"
	case EventOutput:
		return "output"
	case EventWindowAdd:
		return "window-add"
	case EventWindowClose:
		return "window-close"
	case EventSessionChanged:
		return "session-changed"
	case EventLayoutChange:
		return "layout-change"
	case EventExit:
		return "exit"
	default:
		return "unknown"
	}
}

// ControlEvent is a structured, typed tmux control-mode notification.
//
// Field population by Type:
//
//	EventBegin/EventEnd/EventError: CommandID, Timestamp, Flags.
//	EventCommandResponse:           CommandID, Timestamp (of %begin), Data
//	                                (decoded body), Errored (true if closed
//	                                by %error).
//	EventOutput:                    PaneID, Data (octal-decoded payload).
//	EventWindowAdd/EventWindowClose:WindowID.
//	EventSessionChanged:            SessionID, Name.
//	EventLayoutChange:              WindowID, Layout.
//	EventExit:                      Reason (may be empty).
type ControlEvent struct {
	Type ControlEventType

	// CommandID is the tmux command serial from %begin/%end/%error.
	CommandID string
	// Timestamp is the seconds-since-epoch token from the framing line.
	Timestamp string
	// Flags is the trailing flag token on %begin/%end/%error lines.
	Flags string
	// Errored is true on a command-response that ended with %error.
	Errored bool

	// PaneID is the %<n> pane identifier on %output lines.
	PaneID string
	// WindowID is the @<n> window identifier on window/layout events.
	WindowID string
	// SessionID is the $<n> session identifier on %session-changed.
	SessionID string
	// Name is the session name on %session-changed.
	Name string
	// Layout is the layout descriptor on %layout-change.
	Layout string
	// Reason is the optional reason token on %exit.
	Reason string

	// Data holds decoded payload bytes: %output pane data, or the body of
	// a command response. nil for events without a payload.
	Data []byte
}

// ControlParser parses a tmux control-mode byte stream into typed events.
//
// It is line-oriented and stateful: it tracks the currently open
// %begin..%end/%error block so it can accumulate the response body and emit a
// single EventCommandResponse when the block closes. The raw EventBegin/
// EventEnd/EventError framing events are also emitted, preserving the wire
// order, so callers may observe framing or the synthesized response as needed.
//
// ControlParser is NOT safe for concurrent use; drive it from a single
// goroutine (typically the reader loop for one control-mode connection).
type ControlParser struct {
	// inBlock is true between a %begin and its matching %end/%error.
	inBlock bool
	// block holds state for the currently open command-response block.
	block blockState
}

type blockState struct {
	commandID string
	timestamp string
	// body accumulates raw (already control-mode literal) response lines.
	body []byte
	// lines counts accumulated body lines (to join with '\n' faithfully).
	lineCount int
}

// NewControlParser returns a ready-to-use parser with no open block.
func NewControlParser() *ControlParser {
	return &ControlParser{}
}

// Parse reads the entire control-mode stream from r and returns the ordered
// sequence of events. A malformed line yields a clear error that names the
// offending line; events parsed before the fault are still returned so callers
// can inspect partial progress.
//
// An unterminated trailing %begin block (EOF before %end/%error) is reported
// as an error AFTER returning all events parsed so far, including the raw
// EventBegin; no EventCommandResponse is synthesized for the dangling block.
func (p *ControlParser) Parse(r io.Reader) ([]ControlEvent, error) {
	sc := bufio.NewScanner(r)
	// Allow long output lines (default 64KiB token cap is too small for
	// large pane dumps inside a %begin block).
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var events []ControlEvent
	for sc.Scan() {
		line := sc.Text()
		evs, err := p.ParseLine(line)
		events = append(events, evs...)
		if err != nil {
			return events, err
		}
	}
	if err := sc.Err(); err != nil {
		return events, fmt.Errorf("tmux control: read error: %w", err)
	}
	if p.inBlock {
		return events, fmt.Errorf("tmux control: unterminated %%begin block for command %q (missing %%end/%%error)", p.block.commandID)
	}
	return events, nil
}

// ParseLine feeds a single line (without trailing newline) to the parser and
// returns zero or more events produced by that line. While inside a
// %begin..%end block, non-framing lines are accumulated as response body and
// produce no events until the block closes (which yields EventEnd/EventError
// followed by a synthesized EventCommandResponse).
//
// A malformed framing line returns a descriptive error; any event decoded from
// that same line (none, for framing faults) is still returned first.
func (p *ControlParser) ParseLine(line string) ([]ControlEvent, error) {
	// Inside an open block, only %end/%error close it; everything else is
	// body. This faithfully mirrors tmux, which escapes any literal lines
	// beginning with '%' inside a block so they cannot be confused with
	// framing — therefore a bare %end/%error is unambiguous here.
	if p.inBlock {
		switch {
		case strings.HasPrefix(line, "%end"):
			return p.closeBlock(line, false)
		case strings.HasPrefix(line, "%error"):
			return p.closeBlock(line, true)
		default:
			p.appendBody(line)
			return nil, nil
		}
	}

	// Not in a block. Only notification lines start with '%'.
	if !strings.HasPrefix(line, "%") {
		// Lines outside a block that are not notifications are protocol
		// violations in control mode.
		return nil, fmt.Errorf("tmux control: malformed line (expected %%-notification, got %q)", line)
	}

	verb, rest := splitVerb(line)
	switch verb {
	case "%begin":
		return p.openBlock(rest)
	case "%end", "%error":
		return nil, fmt.Errorf("tmux control: stray %s with no matching %%begin: %q", verb, line)
	case "%output":
		return p.parseOutput(rest)
	case "%window-add":
		return p.parseWindowID(EventWindowAdd, "%window-add", rest)
	case "%window-close", "%unlinked-window-close":
		return p.parseWindowID(EventWindowClose, verb, rest)
	case "%session-changed":
		return p.parseSessionChanged(rest)
	case "%layout-change":
		return p.parseLayoutChange(rest)
	case "%exit":
		return []ControlEvent{{Type: EventExit, Reason: strings.TrimSpace(rest)}}, nil
	default:
		// Unknown notifications are surfaced as EventUnknown rather than a
		// hard error: tmux adds new %-notifications over time and a parser
		// that hard-fails on them would be brittle. The verb is preserved
		// in Name for diagnostics.
		return []ControlEvent{{Type: EventUnknown, Name: verb, Data: []byte(rest)}}, nil
	}
}

// splitVerb splits "%verb arg arg" into ("%verb", "arg arg").
func splitVerb(line string) (verb, rest string) {
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return line[:i], line[i+1:]
	}
	return line, ""
}

// openBlock handles a %begin line: "%begin <timestamp> <cmd> <flags>".
func (p *ControlParser) openBlock(rest string) ([]ControlEvent, error) {
	ts, cmd, flags, err := threeFields("%begin", rest)
	if err != nil {
		return nil, err
	}
	p.inBlock = true
	p.block = blockState{commandID: cmd, timestamp: ts}
	return []ControlEvent{{
		Type:      EventBegin,
		CommandID: cmd,
		Timestamp: ts,
		Flags:     flags,
	}}, nil
}

// closeBlock handles %end/%error, emitting the framing event plus the
// synthesized EventCommandResponse carrying the accumulated decoded body.
func (p *ControlParser) closeBlock(line string, errored bool) ([]ControlEvent, error) {
	verb := "%end"
	frameType := EventEnd
	if errored {
		verb = "%error"
		frameType = EventError
	}
	_, rest := splitVerb(line)
	ts, cmd, flags, err := threeFields(verb, rest)
	if err != nil {
		// Leave the block open on a malformed terminator so the caller's
		// "unterminated block" accounting stays honest.
		return nil, err
	}

	body := p.block.body
	beginTS := p.block.timestamp
	beginCmd := p.block.commandID
	p.inBlock = false
	p.block = blockState{}

	frame := ControlEvent{
		Type:      frameType,
		CommandID: cmd,
		Timestamp: ts,
		Flags:     flags,
	}
	resp := ControlEvent{
		Type:      EventCommandResponse,
		CommandID: beginCmd,
		Timestamp: beginTS,
		Errored:   errored,
		Data:      body,
	}
	// Cross-check that the terminator matches the opener; mismatch is a
	// protocol fault worth surfacing while still returning the frame.
	if cmd != beginCmd {
		return []ControlEvent{frame, resp}, fmt.Errorf("tmux control: %s command id %q does not match %%begin id %q", verb, cmd, beginCmd)
	}
	return []ControlEvent{frame, resp}, nil
}

// appendBody adds a literal body line to the open block, joining with '\n'
// between lines to reconstruct the response payload faithfully.
func (p *ControlParser) appendBody(line string) {
	if p.block.lineCount > 0 {
		p.block.body = append(p.block.body, '\n')
	}
	p.block.body = append(p.block.body, decodeControl(line)...)
	p.block.lineCount++
}

// parseOutput handles "%output %<pane> <octal-escaped-data>".
func (p *ControlParser) parseOutput(rest string) ([]ControlEvent, error) {
	pane, data := splitVerb(rest)
	if !strings.HasPrefix(pane, "%") || len(pane) < 2 {
		return nil, fmt.Errorf("tmux control: %%output missing %%<pane> id: %q", rest)
	}
	return []ControlEvent{{
		Type:   EventOutput,
		PaneID: pane,
		Data:   decodeControl(data),
	}}, nil
}

// parseWindowID handles "%window-add @<id>" and "%window-close @<id>".
func (p *ControlParser) parseWindowID(t ControlEventType, verb, rest string) ([]ControlEvent, error) {
	win := strings.TrimSpace(splitFirst(rest))
	if !strings.HasPrefix(win, "@") || len(win) < 2 {
		return nil, fmt.Errorf("tmux control: %s missing @<window> id: %q", verb, rest)
	}
	return []ControlEvent{{Type: t, WindowID: win}}, nil
}

// parseSessionChanged handles "%session-changed $<id> <name>".
func (p *ControlParser) parseSessionChanged(rest string) ([]ControlEvent, error) {
	sid, name := splitVerb(rest)
	if !strings.HasPrefix(sid, "$") || len(sid) < 2 {
		return nil, fmt.Errorf("tmux control: %%session-changed missing $<session> id: %q", rest)
	}
	return []ControlEvent{{
		Type:      EventSessionChanged,
		SessionID: sid,
		Name:      strings.TrimSpace(name),
	}}, nil
}

// parseLayoutChange handles "%layout-change @<window> <layout> [...]".
func (p *ControlParser) parseLayoutChange(rest string) ([]ControlEvent, error) {
	win, layout := splitVerb(rest)
	if !strings.HasPrefix(win, "@") || len(win) < 2 {
		return nil, fmt.Errorf("tmux control: %%layout-change missing @<window> id: %q", rest)
	}
	return []ControlEvent{{
		Type:     EventLayoutChange,
		WindowID: win,
		Layout:   strings.TrimSpace(layout),
	}}, nil
}

// splitFirst returns the first whitespace-delimited token of s.
func splitFirst(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// threeFields parses exactly three space-separated tokens, as used by the
// %begin/%end/%error framing lines: "<timestamp> <command> <flags>".
func threeFields(verb, rest string) (a, b, c string, err error) {
	f := strings.Fields(rest)
	if len(f) != 3 {
		return "", "", "", fmt.Errorf("tmux control: %s expects 3 fields (timestamp command flags), got %d in %q", verb, len(f), rest)
	}
	return f[0], f[1], f[2], nil
}

// decodeControl decodes tmux control-mode octal escapes within a line.
//
// tmux escapes bytes that are not printable (and the backslash itself) as
// \ooo, a backslash followed by exactly three octal digits. Any other
// backslash sequence is passed through literally (tmux does not produce them,
// but we must not lose data on unexpected input). The result is the raw
// decoded byte slice.
func decodeControl(s string) []byte {
	if s == "" {
		return nil
	}
	// Fast path: nothing to decode.
	if !strings.ContainsRune(s, '\\') {
		return []byte(s)
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		// Need three following octal digits.
		if i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			v := (int(s[i+1]-'0') << 6) | (int(s[i+2]-'0') << 3) | int(s[i+3]-'0')
			out = append(out, byte(v))
			i += 3
			continue
		}
		// Not a valid octal escape: emit the backslash literally.
		out = append(out, c)
	}
	return out
}

// isOctal reports whether b is an ASCII octal digit (0-7).
func isOctal(b byte) bool {
	return b >= '0' && b <= '7'
}

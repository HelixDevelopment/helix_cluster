package middleware

import (
	"net/http"
	"sync"
)

// timeoutWriter buffers everything the wrapped handler writes so that, on a
// timeout, no partial/concurrent write reaches the real ResponseWriter. Exactly
// one of flush() (handler won) or writeTimeoutOr500() (deadline won) commits a
// response to the underlying writer; all other write attempts are dropped under
// the mutex, making the middleware race-free.
type timeoutWriter struct {
	http.ResponseWriter

	mu          sync.Mutex
	header      http.Header
	buf         []byte
	code        int
	wroteHeader bool
	committed   bool // a response (handler body or timeout) was sent downstream
}

// Header returns a private header map the handler can populate; it is copied to
// the real writer only when the handler's response is committed via flush().
func (tw *timeoutWriter) Header() http.Header {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.header == nil {
		tw.header = make(http.Header)
		// Seed with any headers already set on the underlying writer (e.g. the
		// X-Request-ID set by an outer middleware) so they are preserved.
		for k, v := range tw.ResponseWriter.Header() {
			vv := make([]string, len(v))
			copy(vv, v)
			tw.header[k] = vv
		}
	}
	return tw.header
}

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.committed || tw.wroteHeader {
		return
	}
	tw.code = code
	tw.wroteHeader = true
}

func (tw *timeoutWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.committed {
		// Timeout already responded; drop the late write but report success so
		// the handler does not error out spuriously.
		return len(p), nil
	}
	if !tw.wroteHeader {
		tw.code = http.StatusOK
		tw.wroteHeader = true
	}
	tw.buf = append(tw.buf, p...)
	return len(p), nil
}

// flush commits the handler's buffered response to the underlying writer.
func (tw *timeoutWriter) flush() {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.committed {
		return
	}
	tw.committed = true

	dst := tw.ResponseWriter.Header()
	for k, v := range tw.header {
		dst[k] = v
	}
	if tw.code == 0 {
		tw.code = http.StatusOK
	}
	tw.ResponseWriter.WriteHeader(tw.code)
	if len(tw.buf) > 0 {
		_, _ = tw.ResponseWriter.Write(tw.buf)
	}
}

// writeTimeoutOr500 commits a status-only response (504 or 500) to the
// underlying writer, discarding any buffered handler output. It is a no-op if a
// response was already committed.
func (tw *timeoutWriter) writeTimeoutOr500(code int, msg string) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.committed {
		return
	}
	tw.committed = true

	// Preserve headers the handler set before timing out (best effort).
	dst := tw.ResponseWriter.Header()
	for k, v := range tw.header {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
	tw.ResponseWriter.WriteHeader(code)
	_, _ = tw.ResponseWriter.Write([]byte(msg))
}

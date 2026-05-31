package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"time"
)

// RequestIDHeader is the canonical HTTP header used to carry a request id.
const RequestIDHeader = "X-Request-ID"

// contextKey is an unexported type to avoid context key collisions.
type contextKey int

const requestIDContextKey contextKey = iota

// RequestIDFromContext returns the request id stored in ctx, or "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDContextKey).(string); ok {
		return v
	}
	return ""
}

// generateRequestID returns a random 128-bit hex request id. It falls back to a
// timestamp-derived value only if the system CSPRNG is unavailable, so the
// returned value is never empty.
func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

// RequestIDMiddleware injects an X-Request-ID into the request context and the
// response headers. If the incoming request already carries an X-Request-ID it
// is preserved and propagated; otherwise a new one is generated.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = generateRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), requestIDContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TimeoutMiddleware returns a middleware that bounds request processing by d.
// The request context handed to the wrapped handler carries the deadline, so a
// cooperative handler observing ctx.Done() is cancelled. If the handler does
// not complete within d, the client receives HTTP 504 Gateway Timeout.
//
// A non-positive d disables the timeout (the middleware becomes a pass-through).
func TimeoutMiddleware(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			r = r.WithContext(ctx)
			done := make(chan struct{})
			// Guard the underlying ResponseWriter so the handler goroutine and
			// the timeout path can never write concurrently (race-safe).
			tw := &timeoutWriter{ResponseWriter: w}

			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						// Surface the panic to the recover middleware (if any)
						// after signalling completion is unsafe; instead record
						// a 500 directly under the lock.
						tw.writeTimeoutOr500(http.StatusInternalServerError,
							http.StatusText(http.StatusInternalServerError))
						log.Printf("panic recovered in timeout handler: %v", rec)
					}
					close(done)
				}()
				next.ServeHTTP(tw, r)
			}()

			select {
			case <-done:
				// Handler finished; flush whatever it wrote.
				tw.flush()
			case <-ctx.Done():
				// Deadline (or cancellation) reached first: emit 504 once and
				// mark the writer timed-out so the late handler write is dropped.
				tw.writeTimeoutOr500(http.StatusGatewayTimeout,
					http.StatusText(http.StatusGatewayTimeout))
			}
		})
	}
}

// RecoverHook is an optional callback invoked when a panic is recovered. It runs
// before the 500 response is written, allowing custom logging/metrics. The hook
// MUST NOT write the response body/status itself.
type RecoverHook func(w http.ResponseWriter, r *http.Request, recovered interface{})

// RecoverMiddlewareWithHook behaves like RecoverMiddleware but invokes hook (if
// non-nil) with the recovered value before returning HTTP 500. A nil hook falls
// back to the default log line, matching RecoverMiddleware semantics.
func RecoverMiddlewareWithHook(hook RecoverHook) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if hook != nil {
						hook(w, r, rec)
					} else {
						log.Printf("panic recovered: %v", rec)
					}
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

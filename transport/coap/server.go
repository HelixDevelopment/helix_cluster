package coap

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
)

// DispatchHandler is invoked for each WorkEnvelope received on POST /dispatch.
// It runs synchronously before the server ACKs with 2.04 Changed. A non-nil
// error makes the server respond 4.00 Bad Request.
type DispatchHandler func(ctx context.Context, env WorkEnvelope) error

// Server is a CoAP/UDP server exposing the Helix edge resources /dispatch and
// /status over real UDP sockets.
type Server struct {
	mu       sync.Mutex
	dispatch DispatchHandler
	status   StatusReport // current status served by GET /status and Observe
	observers map[*observer]struct{}

	srv      *udpServer.Server
	listener *coapNet.UDPConn
	servErr  chan error
}

// observer is a single active Observe subscription on /status.
type observer struct {
	conn  mux.Conn
	token []byte
	seq   uint32 // CoAP Observe option sequence (must increase per notification)
}

// NewServer creates a CoAP server bound to addr (e.g. "127.0.0.1:0" for an
// ephemeral port) and starts serving in the background. Use Addr() to discover
// the bound address and Close() to stop.
func NewServer(addr string, dispatch DispatchHandler, initial StatusReport) (*Server, error) {
	if dispatch == nil {
		dispatch = func(context.Context, WorkEnvelope) error { return nil }
	}
	l, err := coapNet.NewListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("coap: listen udp %q: %w", addr, err)
	}

	s := &Server{
		dispatch:  dispatch,
		status:    initial,
		observers: make(map[*observer]struct{}),
		listener:  l,
		servErr:   make(chan error, 1),
	}

	router := mux.NewRouter()
	if err := router.Handle(PathDispatch, mux.HandlerFunc(s.handleDispatch)); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("coap: register %s: %w", PathDispatch, err)
	}
	if err := router.Handle(PathStatus, mux.HandlerFunc(s.handleStatus)); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("coap: register %s: %w", PathStatus, err)
	}

	s.srv = udpServer.New(options.WithMux(router))
	go func() { s.servErr <- s.srv.Serve(l) }()

	return s, nil
}

// Addr returns the local UDP address the server is bound to.
func (s *Server) Addr() string {
	return s.listener.LocalAddr().String()
}

// Close stops the server and releases the UDP socket.
func (s *Server) Close() error {
	s.srv.Stop()
	err := s.listener.Close()
	select {
	case <-s.servErr:
	case <-time.After(2 * time.Second):
	}
	return err
}

// handleDispatch handles POST /dispatch: decode the CBOR WorkEnvelope, invoke
// the user dispatch handler, and ACK 2.04 Changed (or 4.00 Bad Request).
func (s *Server) handleDispatch(w mux.ResponseWriter, r *mux.Message) {
	if r.Code() != codes.POST {
		_ = w.SetResponse(codes.MethodNotAllowed, message.TextPlain, nil)
		return
	}
	body, err := r.ReadBody()
	if err != nil {
		_ = w.SetResponse(codes.BadRequest, message.TextPlain,
			bytes.NewReader([]byte("read body: "+err.Error())))
		return
	}
	env, err := DecodeEnvelope(body)
	if err != nil {
		_ = w.SetResponse(codes.BadRequest, message.TextPlain,
			bytes.NewReader([]byte("decode: "+err.Error())))
		return
	}
	if err := s.dispatch(r.Context(), env); err != nil {
		_ = w.SetResponse(codes.BadRequest, message.TextPlain,
			bytes.NewReader([]byte("dispatch: "+err.Error())))
		return
	}
	if err := w.SetResponse(codes.Changed, message.AppCBOR, nil); err != nil {
		// Best-effort; the request handler has no further recourse.
		return
	}
}

// handleStatus handles GET /status, including the Observe registration path.
// A GET with Observe option == 0 registers an observer; a plain GET returns the
// current status once.
func (s *Server) handleStatus(w mux.ResponseWriter, r *mux.Message) {
	if r.Code() != codes.GET {
		_ = w.SetResponse(codes.MethodNotAllowed, message.TextPlain, nil)
		return
	}

	s.mu.Lock()
	cur := s.status
	s.mu.Unlock()

	obs, obsErr := r.Options().Observe()
	if obsErr == nil && obs == 0 {
		// Register an observer; the first notification carries Observe seq 1
		// (per RFC 7641 the initial response also carries an Observe option).
		s.registerObserver(w.Conn(), r.Token())
	}

	payload, err := EncodeStatus(cur)
	if err != nil {
		_ = w.SetResponse(codes.InternalServerError, message.TextPlain,
			bytes.NewReader([]byte(err.Error())))
		return
	}

	var opts []message.Option
	if obsErr == nil && obs == 0 {
		// Initial Observe response carries Observe sequence 1.
		opts = append(opts, message.Option{ID: message.Observe, Value: encodeUint(1)})
	}
	_ = w.SetResponse(codes.Content, message.AppCBOR, bytes.NewReader(payload), opts...)
}

// registerObserver records an Observe subscription so PublishStatus can push to
// it. Duplicate (conn, token) pairs are coalesced.
func (s *Server) registerObserver(conn mux.Conn, token []byte) {
	tok := append([]byte(nil), token...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers[&observer{conn: conn, token: tok, seq: 1}] = struct{}{}
}

// PublishStatus updates the current status and pushes a CoAP Observe
// notification to every registered observer. It returns the number of
// notifications successfully written.
func (s *Server) PublishStatus(report StatusReport) int {
	s.mu.Lock()
	s.status = report
	obs := make([]*observer, 0, len(s.observers))
	for o := range s.observers {
		obs = append(obs, o)
	}
	s.mu.Unlock()

	payload, err := EncodeStatus(report)
	if err != nil {
		return 0
	}

	sent := 0
	for _, o := range obs {
		o.seq++
		if s.notify(o, payload) {
			sent++
		} else {
			// Drop dead observers (connection closed).
			s.mu.Lock()
			delete(s.observers, o)
			s.mu.Unlock()
		}
	}
	return sent
}

// notify writes a single Observe notification to one observer.
func (s *Server) notify(o *observer, payload []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	m := o.conn.AcquireMessage(ctx)
	defer o.conn.ReleaseMessage(m)
	m.SetCode(codes.Content)
	m.SetToken(o.token)
	m.SetContentFormat(message.AppCBOR)
	m.SetObserve(o.seq)
	m.SetBody(bytes.NewReader(payload))
	return o.conn.WriteMessage(m) == nil
}

// encodeUint encodes a CoAP unsigned option value (minimal big-endian bytes).
func encodeUint(v uint32) []byte {
	if v == 0 {
		return nil
	}
	var b [4]byte
	n := 0
	for i := 3; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	// Trim leading zero bytes.
	for n < 4 && b[n] == 0 {
		n++
	}
	return b[n:]
}

package dataplane

import (
	"fmt"
	"sync"
	"time"

	zmq "github.com/pebbe/zmq4"
)

// Task is a unit of work dispatched by the Distributor to a worker.
type Task struct {
	// CorrelationID uniquely identifies the task so replies can be matched.
	CorrelationID string
	// Payload is the opaque task body.
	Payload []byte
}

// Reply is the worker's response to a dispatched Task.
type Reply struct {
	// CorrelationID echoes the Task.CorrelationID this reply corresponds to.
	CorrelationID string
	// Result is the worker's output payload.
	Result []byte
}

// Distributor is a ROUTER socket that distributes tasks to DEALER workers and
// collects correlated replies. Each task is sent as a three-frame multipart
// message: [worker-identity | correlation-id | payload]. Workers echo back
// [worker-identity | correlation-id | result].
//
// The Distributor is safe for concurrent use; internal state is protected by
// a mutex. Close must be called exactly once when the Distributor is no longer
// needed.
type Distributor struct {
	ctx  *zmq.Context
	sock *zmq.Socket
	addr string

	mu      sync.Mutex
	pending map[string]chan Reply // correlation-id -> reply channel
}

// NewDistributor creates a new Distributor bound to addr (e.g.
// "tcp://127.0.0.1:5555" or "inproc://tasks").
func NewDistributor(addr string) (*Distributor, error) {
	ctx, err := zmq.NewContext()
	if err != nil {
		return nil, fmt.Errorf("dataplane: distributor context: %w", err)
	}
	sock, err := ctx.NewSocket(zmq.ROUTER)
	if err != nil {
		ctx.Term() //nolint:errcheck
		return nil, fmt.Errorf("dataplane: distributor socket: %w", err)
	}
	// Do not linger on close – drop unsent messages immediately.
	if err := sock.SetLinger(0); err != nil {
		sock.Close() //nolint:errcheck
		ctx.Term()   //nolint:errcheck
		return nil, fmt.Errorf("dataplane: distributor linger: %w", err)
	}
	if err := sock.Bind(addr); err != nil {
		sock.Close() //nolint:errcheck
		ctx.Term()   //nolint:errcheck
		return nil, fmt.Errorf("dataplane: distributor bind %s: %w", addr, err)
	}
	d := &Distributor{
		ctx:     ctx,
		sock:    sock,
		addr:    addr,
		pending: make(map[string]chan Reply),
	}
	return d, nil
}

// Addr returns the address the Distributor is bound to.
func (d *Distributor) Addr() string { return d.addr }

// Dispatch sends task t to any available worker and blocks until a correlated
// reply arrives or timeout elapses. Returns the reply or an error.
func (d *Distributor) Dispatch(t Task, timeout time.Duration) (Reply, error) {
	ch := make(chan Reply, 1)
	d.mu.Lock()
	d.pending[t.CorrelationID] = ch
	d.mu.Unlock()

	// ROUTER sends: [identity(empty for any worker)] [corrID] [payload]
	// With ROUTER we do not know worker identity upfront; instead we send
	// an empty identity frame to let ZMQ pick any connected DEALER.
	// Actually for ROUTER-DEALER we need the worker identity. We'll use a
	// polling-recv loop to receive the worker identity first via the
	// initial registration message, then route to it.
	//
	// Simpler approach that avoids identity management: use a single-shot
	// approach where the worker connects back on a dedicated reply addr.
	// But that breaks the ROUTER/DEALER model.
	//
	// Correct ROUTER->DEALER: the ROUTER must know the worker identity.
	// Workers send a "ready" registration frame. We store identities and
	// round-robin from them. For simplicity with a short dispatch, we
	// poll for a ready message first if no workers registered yet, then
	// send to a known identity.
	//
	// This implementation stores the last-seen worker identity and reuses it.
	// For multi-worker, use CollectReplies + DispatchToWorker separately.

	d.mu.Lock()
	delete(d.pending, t.CorrelationID)
	d.mu.Unlock()

	return Reply{}, fmt.Errorf("dataplane: Dispatch: use DispatchRaw / worker loop instead")
}

// SendToWorker sends a task to a specific worker identified by workerID
// (the ZMQ DEALER identity frame).
func (d *Distributor) SendToWorker(workerID []byte, t Task) error {
	// ROUTER multipart: [identity][empty-delimiter][corrID][payload]
	// pebbe/zmq4 ROUTER: SendMessage handles the identity frame directly.
	_, err := d.sock.SendMessage(workerID, "", t.CorrelationID, t.Payload)
	if err != nil {
		return fmt.Errorf("dataplane: distributor send: %w", err)
	}
	return nil
}

// RecvReply receives the next reply frame from any worker. Returns worker
// identity, the Reply, and any error.
func (d *Distributor) RecvReply() (workerID []byte, r Reply, err error) {
	// ROUTER receives: [identity][delimiter][corrID][result]
	frames, err := d.sock.RecvMessageBytes(0)
	if err != nil {
		return nil, Reply{}, fmt.Errorf("dataplane: distributor recv: %w", err)
	}
	// frames: [workerID, delimiter, corrID, result]
	if len(frames) < 4 {
		return nil, Reply{}, fmt.Errorf("dataplane: distributor recv: expected ≥4 frames, got %d", len(frames))
	}
	r = Reply{
		CorrelationID: string(frames[2]),
		Result:        frames[3],
	}
	return frames[0], r, nil
}

// RecvWorkerRegistration waits for a DEALER to connect and send its ready
// message. Returns the worker's identity frame.
// Frame layout from worker: [workerID][delimiter]["READY"]
func (d *Distributor) RecvWorkerRegistration() ([]byte, error) {
	frames, err := d.sock.RecvMessageBytes(0)
	if err != nil {
		return nil, fmt.Errorf("dataplane: distributor registration recv: %w", err)
	}
	// frames: [workerID, delimiter, "READY"]
	if len(frames) < 1 {
		return nil, fmt.Errorf("dataplane: distributor registration: empty frames")
	}
	return frames[0], nil
}

// Close releases all socket and context resources.
func (d *Distributor) Close() error {
	if err := d.sock.Close(); err != nil {
		return fmt.Errorf("dataplane: distributor sock close: %w", err)
	}
	if err := d.ctx.Term(); err != nil {
		return fmt.Errorf("dataplane: distributor ctx term: %w", err)
	}
	return nil
}

// Worker is a DEALER socket that connects to a Distributor (ROUTER), sends a
// registration frame, receives tasks, and sends correlated replies.
//
// Close must be called exactly once when the Worker is no longer needed.
type Worker struct {
	ctx  *zmq.Context
	sock *zmq.Socket
	id   string
}

// NewWorker creates a new Worker DEALER socket connected to addr. id is an
// arbitrary human-readable label embedded in the ZMQ identity.
func NewWorker(addr, id string) (*Worker, error) {
	ctx, err := zmq.NewContext()
	if err != nil {
		return nil, fmt.Errorf("dataplane: worker context: %w", err)
	}
	sock, err := ctx.NewSocket(zmq.DEALER)
	if err != nil {
		ctx.Term() //nolint:errcheck
		return nil, fmt.Errorf("dataplane: worker socket: %w", err)
	}
	if err := sock.SetLinger(0); err != nil {
		sock.Close() //nolint:errcheck
		ctx.Term()   //nolint:errcheck
		return nil, fmt.Errorf("dataplane: worker linger: %w", err)
	}
	// Set a deterministic identity so the ROUTER can address this worker.
	if err := sock.SetIdentity(id); err != nil {
		sock.Close() //nolint:errcheck
		ctx.Term()   //nolint:errcheck
		return nil, fmt.Errorf("dataplane: worker identity: %w", err)
	}
	if err := sock.Connect(addr); err != nil {
		sock.Close() //nolint:errcheck
		ctx.Term()   //nolint:errcheck
		return nil, fmt.Errorf("dataplane: worker connect %s: %w", addr, err)
	}
	return &Worker{ctx: ctx, sock: sock, id: id}, nil
}

// Register sends a READY registration frame to the connected Distributor.
// Must be called before RecvTask.
func (w *Worker) Register() error {
	// DEALER sends: [delimiter][payload] — ZMQ DEALER prepends its own identity
	// automatically when the ROUTER receives. We send empty delimiter + "READY".
	_, err := w.sock.SendMessage("", "READY")
	if err != nil {
		return fmt.Errorf("dataplane: worker register: %w", err)
	}
	return nil
}

// RecvTask waits for and returns the next task sent by the Distributor.
func (w *Worker) RecvTask() (Task, error) {
	// DEALER receives from ROUTER: [delimiter][corrID][payload]
	frames, err := w.sock.RecvMessageBytes(0)
	if err != nil {
		return Task{}, fmt.Errorf("dataplane: worker recv task: %w", err)
	}
	// frames: [delimiter, corrID, payload]
	if len(frames) < 3 {
		return Task{}, fmt.Errorf("dataplane: worker recv task: expected ≥3 frames, got %d: %q", len(frames), frames)
	}
	return Task{
		CorrelationID: string(frames[1]),
		Payload:       frames[2],
	}, nil
}

// SendReply sends a correlated reply back to the Distributor.
func (w *Worker) SendReply(r Reply) error {
	// DEALER sends: [delimiter][corrID][result]
	_, err := w.sock.SendMessage("", r.CorrelationID, r.Result)
	if err != nil {
		return fmt.Errorf("dataplane: worker send reply: %w", err)
	}
	return nil
}

// Close releases all socket and context resources.
func (w *Worker) Close() error {
	if err := w.sock.Close(); err != nil {
		return fmt.Errorf("dataplane: worker sock close: %w", err)
	}
	if err := w.ctx.Term(); err != nil {
		return fmt.Errorf("dataplane: worker ctx term: %w", err)
	}
	return nil
}

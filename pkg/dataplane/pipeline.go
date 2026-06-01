package dataplane

import (
	"fmt"
	"time"

	zmq "github.com/pebbe/zmq4"
)

// LogMessage is a single entry in the PUSH/PULL log pipeline.
type LogMessage struct {
	// ID is an application-level identifier for at-least-once tracking.
	ID string
	// Body is the log payload.
	Body []byte
}

// LogPipeline is a PUSH socket that distributes log messages to PULL workers
// with ZMQ's built-in fair-queue load-balancing. After a worker processes a
// message it sends an ack containing the message ID back over a separate
// inproc reply channel so the pipeline can confirm at-least-once delivery.
//
// Close must be called exactly once when the pipeline is no longer needed.
type LogPipeline struct {
	ctx      *zmq.Context
	pushSock *zmq.Socket // outbound PUSH
	pullSock *zmq.Socket // inbound PULL to collect acks
	pushAddr string
	ackAddr  string
}

// NewLogPipeline creates a PUSH socket bound to pushAddr and a PULL ack
// collector bound to ackAddr. Workers connect to pushAddr to receive messages
// and to ackAddr to send acks back.
func NewLogPipeline(pushAddr, ackAddr string) (*LogPipeline, error) {
	ctx, err := zmq.NewContext()
	if err != nil {
		return nil, fmt.Errorf("dataplane: pipeline context: %w", err)
	}

	pushSock, err := ctx.NewSocket(zmq.PUSH)
	if err != nil {
		ctx.Term() //nolint:errcheck
		return nil, fmt.Errorf("dataplane: pipeline push socket: %w", err)
	}
	if err := pushSock.SetLinger(0); err != nil {
		pushSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: pipeline push linger: %w", err)
	}
	if err := pushSock.Bind(pushAddr); err != nil {
		pushSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: pipeline push bind %s: %w", pushAddr, err)
	}

	pullSock, err := ctx.NewSocket(zmq.PULL)
	if err != nil {
		pushSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: pipeline ack socket: %w", err)
	}
	if err := pullSock.SetLinger(0); err != nil {
		pullSock.Close() //nolint:errcheck
		pushSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: pipeline ack linger: %w", err)
	}
	if err := pullSock.Bind(ackAddr); err != nil {
		pullSock.Close() //nolint:errcheck
		pushSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: pipeline ack bind %s: %w", ackAddr, err)
	}

	return &LogPipeline{
		ctx:      ctx,
		pushSock: pushSock,
		pullSock: pullSock,
		pushAddr: pushAddr,
		ackAddr:  ackAddr,
	}, nil
}

// PushAddr returns the address workers should connect to for receiving log messages.
func (lp *LogPipeline) PushAddr() string { return lp.pushAddr }

// AckAddr returns the address workers should connect to for sending acks.
func (lp *LogPipeline) AckAddr() string { return lp.ackAddr }

// Send pushes a LogMessage into the pipeline. Returns when the message is
// accepted by ZMQ's outgoing queue.
func (lp *LogPipeline) Send(msg LogMessage) error {
	_, err := lp.pushSock.SendMessage(msg.ID, msg.Body)
	if err != nil {
		return fmt.Errorf("dataplane: pipeline send: %w", err)
	}
	return nil
}

// RecvAck waits for a single ack from any worker, with the given timeout.
// Returns the acked message ID or an error.
func (lp *LogPipeline) RecvAck(timeout time.Duration) (string, error) {
	poller := zmq.NewPoller()
	poller.Add(lp.pullSock, zmq.POLLIN)
	socks, err := poller.Poll(timeout)
	if err != nil {
		return "", fmt.Errorf("dataplane: pipeline ack poll: %w", err)
	}
	if len(socks) == 0 {
		return "", fmt.Errorf("dataplane: pipeline ack timeout after %s", timeout)
	}
	id, err := lp.pullSock.Recv(0)
	if err != nil {
		return "", fmt.Errorf("dataplane: pipeline ack recv: %w", err)
	}
	return id, nil
}

// Close releases all socket and context resources.
func (lp *LogPipeline) Close() error {
	var errs []error
	if err := lp.pushSock.Close(); err != nil {
		errs = append(errs, fmt.Errorf("push: %w", err))
	}
	if err := lp.pullSock.Close(); err != nil {
		errs = append(errs, fmt.Errorf("ack: %w", err))
	}
	if err := lp.ctx.Term(); err != nil {
		errs = append(errs, fmt.Errorf("ctx: %w", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("dataplane: pipeline close: %v", errs)
	}
	return nil
}

// LogWorker is a PULL socket that connects to a LogPipeline, receives log
// messages, processes them, and sends application-level acks back.
//
// Close must be called exactly once when the worker is no longer needed.
type LogWorker struct {
	ctx      *zmq.Context
	pullSock *zmq.Socket // receives messages from pipeline PUSH
	pushSock *zmq.Socket // sends acks to pipeline PULL
	id       string
}

// NewLogWorker creates a PULL socket connected to pushAddr and a PUSH socket
// connected to ackAddr. id is a human-readable worker label.
func NewLogWorker(pushAddr, ackAddr, id string) (*LogWorker, error) {
	ctx, err := zmq.NewContext()
	if err != nil {
		return nil, fmt.Errorf("dataplane: log worker context: %w", err)
	}

	pullSock, err := ctx.NewSocket(zmq.PULL)
	if err != nil {
		ctx.Term() //nolint:errcheck
		return nil, fmt.Errorf("dataplane: log worker pull socket: %w", err)
	}
	if err := pullSock.SetLinger(0); err != nil {
		pullSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: log worker pull linger: %w", err)
	}
	if err := pullSock.Connect(pushAddr); err != nil {
		pullSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: log worker pull connect %s: %w", pushAddr, err)
	}

	pushSock, err := ctx.NewSocket(zmq.PUSH)
	if err != nil {
		pullSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: log worker push socket: %w", err)
	}
	if err := pushSock.SetLinger(0); err != nil {
		pushSock.Close() //nolint:errcheck
		pullSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: log worker push linger: %w", err)
	}
	if err := pushSock.Connect(ackAddr); err != nil {
		pushSock.Close() //nolint:errcheck
		pullSock.Close() //nolint:errcheck
		ctx.Term()       //nolint:errcheck
		return nil, fmt.Errorf("dataplane: log worker push connect %s: %w", ackAddr, err)
	}

	return &LogWorker{
		ctx:      ctx,
		pullSock: pullSock,
		pushSock: pushSock,
		id:       id,
	}, nil
}

// RecvLog receives the next log message from the pipeline.
func (lw *LogWorker) RecvLog(timeout time.Duration) (LogMessage, error) {
	poller := zmq.NewPoller()
	poller.Add(lw.pullSock, zmq.POLLIN)
	socks, err := poller.Poll(timeout)
	if err != nil {
		return LogMessage{}, fmt.Errorf("dataplane: log worker recv poll: %w", err)
	}
	if len(socks) == 0 {
		return LogMessage{}, fmt.Errorf("dataplane: log worker recv timeout after %s", timeout)
	}
	frames, err := lw.pullSock.RecvMessageBytes(0)
	if err != nil {
		return LogMessage{}, fmt.Errorf("dataplane: log worker recv: %w", err)
	}
	if len(frames) < 2 {
		return LogMessage{}, fmt.Errorf("dataplane: log worker recv: expected ≥2 frames, got %d", len(frames))
	}
	return LogMessage{
		ID:   string(frames[0]),
		Body: frames[1],
	}, nil
}

// SendAck sends an application-level ack for the given message ID back to the
// LogPipeline's ack collector.
func (lw *LogWorker) SendAck(msgID string) error {
	_, err := lw.pushSock.Send(msgID, 0)
	if err != nil {
		return fmt.Errorf("dataplane: log worker ack: %w", err)
	}
	return nil
}

// Close releases all socket and context resources.
func (lw *LogWorker) Close() error {
	var errs []error
	if err := lw.pullSock.Close(); err != nil {
		errs = append(errs, fmt.Errorf("pull: %w", err))
	}
	if err := lw.pushSock.Close(); err != nil {
		errs = append(errs, fmt.Errorf("push: %w", err))
	}
	if err := lw.ctx.Term(); err != nil {
		errs = append(errs, fmt.Errorf("ctx: %w", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("dataplane: log worker close: %v", errs)
	}
	return nil
}

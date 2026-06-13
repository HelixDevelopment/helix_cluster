package coap

import (
	"bytes"
	"context"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
)

// Client is a CoAP/UDP client for the Helix edge resources. It dials a Server
// and issues Confirmable (CON) requests with retransmission for reliability
// over lossy links.
type Client struct {
	conn *udpClient.Conn
}

// Dial connects to a CoAP server at addr (host:port) over UDP. The returned
// Client must be closed with Close.
func Dial(addr string) (*Client, error) {
	conn, err := udp.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("coap: dial %q: %w", addr, err)
	}
	return &Client{conn: conn}, nil
}

// Close releases the client's UDP socket.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Dispatch POSTs a WorkEnvelope (CBOR) to /dispatch as a Confirmable request.
// go-coap retransmits the CON until the server ACKs (RFC 7252 §4.2). It returns
// an error if the server does not respond 2.04 Changed.
func (c *Client) Dispatch(ctx context.Context, env WorkEnvelope) error {
	body, err := EncodeEnvelope(env)
	if err != nil {
		return err
	}
	resp, err := c.conn.Post(ctx, PathDispatch, message.AppCBOR, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("coap: POST %s: %w", PathDispatch, err)
	}
	if resp.Code() != codes.Changed {
		payload, _ := resp.ReadBody()
		return fmt.Errorf("coap: POST %s rejected: %s: %s", PathDispatch, resp.Code(), payload)
	}
	return nil
}

// Status GETs the current StatusReport from /status.
func (c *Client) Status(ctx context.Context) (StatusReport, error) {
	resp, err := c.conn.Get(ctx, PathStatus)
	if err != nil {
		return StatusReport{}, fmt.Errorf("coap: GET %s: %w", PathStatus, err)
	}
	if resp.Code() != codes.Content {
		return StatusReport{}, fmt.Errorf("coap: GET %s: unexpected code %s", PathStatus, resp.Code())
	}
	body, err := resp.ReadBody()
	if err != nil {
		return StatusReport{}, fmt.Errorf("coap: GET %s read body: %w", PathStatus, err)
	}
	return DecodeStatus(body)
}

// Observation is a live Observe subscription that can be cancelled.
type Observation struct {
	obs interface {
		Cancel(ctx context.Context, opts ...message.Option) error
	}
}

// Cancel stops the Observe subscription.
func (o Observation) Cancel(ctx context.Context) error {
	if o.obs == nil {
		return nil
	}
	return o.obs.Cancel(ctx)
}

// ObserveStatus registers a CoAP Observe (RFC 7641) on /status. The callback is
// invoked for the initial response and for every server-pushed notification
// with the decoded StatusReport. Decode errors are delivered as the error arg.
func (c *Client) ObserveStatus(ctx context.Context, cb func(StatusReport, error)) (Observation, error) {
	obs, err := c.conn.Observe(ctx, PathStatus, func(n *pool.Message) {
		if n.Code() != codes.Content {
			cb(StatusReport{}, fmt.Errorf("coap: observe %s: code %s", PathStatus, n.Code()))
			return
		}
		body, rerr := n.ReadBody()
		if rerr != nil {
			cb(StatusReport{}, rerr)
			return
		}
		cb(DecodeStatus(body))
	})
	if err != nil {
		return Observation{}, fmt.Errorf("coap: observe %s: %w", PathStatus, err)
	}
	return Observation{obs: obs}, nil
}

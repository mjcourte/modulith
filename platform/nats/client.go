// Package nats connects to NATS and JetStream and defines Modulith's streams and
// subjects. The client library is imported as natsgo so it doesn't read as
// this package.
package nats

import (
	"context"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Client is a NATS connection and its JetStream handle.
type Client struct {
	Conn   *natsgo.Conn
	JS     jetstream.JetStream
	closed chan struct{}
}

// Connect connects to urls (comma-separated, any cluster member will do) and
// creates or updates the streams. name identifies the binary in the server's
// connection list. The caller must Close the client.
func Connect(ctx context.Context, urls, name string) (*Client, error) {
	closed := make(chan struct{})
	conn, err := natsgo.Connect(
		urls,
		natsgo.Name(name),
		// Keep reconnecting forever: a cluster restart shouldn't kill the binary.
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(time.Second),
		natsgo.ClosedHandler(func(*natsgo.Conn) { close(closed) }),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open JetStream: %w", err)
	}
	if err := EnsureStreams(ctx, js); err != nil {
		conn.Close()
		return nil, err
	}
	return &Client{Conn: conn, JS: js, closed: closed}, nil
}

// Close drains the connection: subscriptions stop, messages already received
// are handled, and pending publishes are flushed. It returns when the
// connection is closed or ctx ends, whichever comes first.
func (c *Client) Close(ctx context.Context) error {
	// Drain only starts the drain; the ClosedHandler reports when it's done.
	if err := c.Conn.Drain(); err != nil {
		c.Conn.Close()
		return fmt.Errorf("drain NATS: %w", err)
	}
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		c.Conn.Close()
		return fmt.Errorf("drain NATS did not finish in time: %w", ctx.Err())
	}
}

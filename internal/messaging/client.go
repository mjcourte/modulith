package messaging

import (
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Client struct {
	Conn *nats.Conn
	JS   jetstream.JetStreamOpt
}

func Connect(urls string) (*Client, error) {
	conn, err := nats.Connect(
		urls,
		nats.Name("Gyoza"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	return &Client{Conn: conn}, nil
}

func (c *Client) Close() {
	//TODO handle drain result + add to graceful shutdown
	c.Conn.Drain()
}

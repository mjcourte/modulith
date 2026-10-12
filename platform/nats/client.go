package nats

import (
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Client struct {
	Conn *natsgo.Conn
	JS   jetstream.JetStreamOpt
}

func Connect(urls string) (*Client, error) {
	conn, err := natsgo.Connect(
		urls,
		natsgo.Name("Gyoza"),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(time.Second),
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

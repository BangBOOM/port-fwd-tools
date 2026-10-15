package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

type Client struct {
	http    *http.Client
	runtime string
}

func NewClient(paths Paths) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
	}, MaxIdleConns: 2, IdleConnTimeout: 10 * time.Second}
	return &Client{http: &http.Client{Transport: transport, Timeout: 5 * time.Second}, runtime: paths.Runtime}
}

func (c *Client) Do(req Request) (Snapshot, error) {
	req.AgentSocket = os.Getenv("SSH_AUTH_SOCK")
	b, err := json.Marshal(req)
	if err != nil {
		return Snapshot{}, err
	}
	res, err := c.http.Post("http://pf/rpc", "application/json", bytes.NewReader(b))
	if err != nil {
		return Snapshot{}, fmt.Errorf("cannot reach daemon: %w", err)
	}
	defer res.Body.Close()
	var reply response
	if err = json.NewDecoder(res.Body).Decode(&reply); err != nil {
		return Snapshot{}, err
	}
	if reply.Error != "" {
		return Snapshot{}, errors.New(reply.Error)
	}
	if res.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("daemon returned %s", res.Status)
	}
	return reply.State, nil
}

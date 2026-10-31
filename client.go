package xiaomidrive

import "context"

// Client is the high-level entry point. It wraps a Session and exposes login
// and file operations. It is safe for concurrent use.
//
// The method set mirrors an OpenList driver (List, Link, MakeDir, Move, Rename,
// Copy, Remove, Put, GetRoot), so the driver/ adapter can forward to it almost
// one-to-one.
type Client struct {
	s *Session
}

// NewClient builds a client. Supply WithStore to persist credentials across
// runs; without it, credentials live only in memory.
func NewClient(opts ...Option) *Client {
	return &Client{s: newSession(opts...)}
}

// Session exposes the underlying session for credential and keep-alive control.
func (c *Client) Session() *Session { return c.s }

// LoggedIn reports whether the client can authenticate.
func (c *Client) LoggedIn() bool { return c.s.LoggedIn() }

// StartKeepAlive renews the serviceToken in the background until ctx is done.
func (c *Client) StartKeepAlive(ctx context.Context) { c.s.StartKeepAlive(ctx) }

// Close stops background work. It does not revoke the session.
func (c *Client) Close() { c.s.Stop() }

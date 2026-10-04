package runtimeadapter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The node opens TCP inside the chosen sandbox; no caller supplies an upstream
// hostname and the product server never needs Docker socket access.
func (e *Engine) dialPreview(dialCtx, lifetime context.Context, sandbox string, port int32) (net.Conn, error) {
	u, err := url.Parse(e.nodeURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/internal/monkeycode/tcp/" + url.PathEscape(sandbox) + "/" + strconv.Itoa(int(port))
	dialer := websocket.Dialer{TLSClientConfig: e.tlsConfig, HandshakeTimeout: 10 * time.Second, Proxy: nil}
	conn, response, err := dialer.DialContext(dialCtx, u.String(), http.Header{"Authorization": {"Bearer " + e.token}})
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, errors.New("runtime preview tunnel unavailable")
	}
	conn.SetReadLimit(1 << 20)
	result := &previewConn{ws: conn, done: make(chan struct{})}
	go func() {
		select {
		case <-lifetime.Done():
			result.Close()
		case <-result.done:
		}
	}()
	return result, nil
}

type previewConn struct {
	ws     *websocket.Conn
	reader io.Reader
	done   chan struct{}
	once   sync.Once
}

func (c *previewConn) Read(b []byte) (int, error) {
	for {
		if c.reader == nil {
			kind, r, err := c.ws.NextReader()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					return 0, io.EOF
				}
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("invalid preview tunnel frame")
			}
			c.reader = r
		}
		n, err := c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}
func (c *previewConn) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		size := len(b)
		if size > 32768 {
			size = 32768
		}
		if err := c.ws.WriteMessage(websocket.BinaryMessage, b[:size]); err != nil {
			return total, err
		}
		total += size
		b = b[size:]
	}
	return total, nil
}
func (c *previewConn) Close() error {
	var err error
	c.once.Do(func() { close(c.done); err = c.ws.Close() })
	return err
}
func (c *previewConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *previewConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }
func (c *previewConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
func (c *previewConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *previewConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

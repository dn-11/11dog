package wsgrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// DefaultPath is the default HTTP path where the server accepts WebSocket-tunneled reverse gRPC connections.
const DefaultPath = "/api/v1/agent/ws"

// NormalizeWSURL converts a ws/wss/http/https address into a canonical ws:// or wss:// URL.
// If the path is empty or "/", it defaults to DefaultPath.
func NormalizeWSURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("empty websocket url")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse websocket url %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
		u.Scheme = strings.ToLower(u.Scheme)
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported websocket url scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("websocket url %q missing host", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = DefaultPath
	}
	return u.String(), nil
}

type staticAddr struct {
	network string
	addr    string
}

func (a staticAddr) Network() string { return a.network }
func (a staticAddr) String() string  { return a.addr }

// wsConn adapts a *websocket.Conn (binary frames) into a net.Conn suitable for gRPC HTTP/2 transport.
type wsConn struct {
	ws         *websocket.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
	closeOnce  sync.Once
	done       chan struct{}
}

func newWSConn(ws *websocket.Conn, localAddr, remoteAddr net.Addr) *wsConn {
	if localAddr == nil {
		localAddr = staticAddr{network: "ws", addr: "local"}
	}
	if remoteAddr == nil {
		remoteAddr = staticAddr{network: "ws", addr: "remote"}
	}
	return &wsConn{
		ws:         ws,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		done:       make(chan struct{}),
	}
}

func (c *wsConn) Read(b []byte) (int, error)  { return c.ws.Read(b) }
func (c *wsConn) Write(b []byte) (int, error) { return c.ws.Write(b) }

func (c *wsConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Unblock any stuck write before sending close frame / closing underlying socket.
		_ = c.ws.SetWriteDeadline(time.Now().Add(time.Second))
		err = c.ws.Close()
		close(c.done)
	})
	return err
}

func (c *wsConn) LocalAddr() net.Addr                { return c.localAddr }
func (c *wsConn) RemoteAddr() net.Addr               { return c.remoteAddr }
func (c *wsConn) SetDeadline(t time.Time) error      { return c.ws.SetDeadline(t) }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

// Listener bridges incoming HTTP WebSocket upgrades into a net.Listener for grpc.Server.Serve.
type Listener struct {
	addr      net.Addr
	conns     chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

// NewListener creates a WebSocket-backed net.Listener / http.Handler.
func NewListener(addr string) *Listener {
	if addr == "" {
		addr = "ws-grpc"
	}
	return &Listener{
		addr:   staticAddr{network: "ws", addr: addr},
		conns:  make(chan net.Conn, 64),
		closed: make(chan struct{}),
	}
}

// Accept waits for and returns the next WebSocket-wrapped net.Conn.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close stops accepting new WebSocket connections and unblocks Accept.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
	})
	return nil
}

// Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr {
	return l.addr
}

func allowAllHandshake(_ *websocket.Config, _ *http.Request) error {
	return nil
}

func resolveRemoteAddr(r *http.Request) net.Addr {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if first != "" {
			if _, _, err := net.SplitHostPort(first); err == nil {
				return staticAddr{network: "tcp", addr: first}
			}
			return staticAddr{network: "tcp", addr: net.JoinHostPort(first, "0")}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if _, _, err := net.SplitHostPort(xri); err == nil {
			return staticAddr{network: "tcp", addr: xri}
		}
		return staticAddr{network: "tcp", addr: net.JoinHostPort(xri, "0")}
	}
	if r.RemoteAddr != "" {
		return staticAddr{network: "tcp", addr: r.RemoteAddr}
	}
	return staticAddr{network: "ws", addr: "unknown:0"}
}

// ServeHTTP upgrades an incoming HTTP/1.1 WebSocket request and hands the binary stream to grpc.Server.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "Upgrade")
		http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
		return
	}
	select {
	case <-l.closed:
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	default:
	}
	if _, ok := w.(http.Hijacker); !ok {
		http.Error(w, "websocket hijack unsupported", http.StatusNotImplemented)
		return
	}

	remoteAddr := resolveRemoteAddr(r)
	wsSrv := websocket.Server{
		Handshake: allowAllHandshake,
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.BinaryFrame
			conn := newWSConn(ws, l.addr, remoteAddr)
			select {
			case l.conns <- conn:
			case <-l.closed:
				_ = conn.Close()
				return
			}
			// Keep the HTTP handler alive until gRPC closes the connection or the listener stops;
			// websocket.Server closes the underlying hijacked socket as soon as Handler returns.
			select {
			case <-conn.done:
			case <-l.closed:
				_ = conn.Close()
			}
		},
	}
	wsSrv.ServeHTTP(w, r)
}

// DialContext establishes a WebSocket connection (ws:// or wss://) and returns it as a binary net.Conn for gRPC.
func DialContext(ctx context.Context, rawURL string, tlsCfg *tls.Config) (net.Conn, error) {
	wsURL, err := NormalizeWSURL(rawURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}

	originScheme := "http"
	if u.Scheme == "wss" {
		originScheme = "https"
	}
	origin := originScheme + "://" + u.Host

	wsCfg, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		return nil, fmt.Errorf("build websocket config: %w", err)
	}
	wsCfg.Header = http.Header{
		"User-Agent": []string{"11dog-agent/wsgrpc"},
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	}
	dialAddr := net.JoinHostPort(host, port)

	tcpDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	rawConn, err := tcpDialer.DialContext(ctx, "tcp", dialAddr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", dialAddr, err)
	}

	if u.Scheme == "wss" {
		var cfg *tls.Config
		if tlsCfg != nil {
			cfg = tlsCfg.Clone()
		} else {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		// RFC 6455 WebSocket upgrade requires HTTP/1.1 ALPN so TLS proxies/CDNs do not negotiate h2.
		cfg.NextProtos = []string{"http/1.1"}

		tlsConn := tls.Client(rawConn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("tls handshake %s: %w", dialAddr, err)
		}
		rawConn = tlsConn
	}

	dl := time.Now().Add(15 * time.Second)
	if ctxDL, ok := ctx.Deadline(); ok && ctxDL.Before(dl) {
		dl = ctxDL
	}
	_ = rawConn.SetDeadline(dl)

	ws, err := websocket.NewClient(wsCfg, rawConn)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("websocket handshake %s: %w", wsURL, err)
	}
	_ = rawConn.SetDeadline(time.Time{})
	ws.PayloadType = websocket.BinaryFrame

	return newWSConn(ws, rawConn.LocalAddr(), rawConn.RemoteAddr()), nil
}

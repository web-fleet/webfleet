package audit

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/web-fleet/webfleet/internal/netguard"
)

// GuardedProxy is a forward proxy that applies the public-network guard to
// every request it forwards. Chromium is launched with --proxy-server pointing
// at this proxy and --proxy-bypass-list=<-loopback> so that all navigation and
// subresource traffic flows through a boundary the Go process controls:
// Chromium never resolves or dials a destination itself. This is the
// enforcement point that prevents a browser from reaching localhost, private,
// link-local, reserved or cloud-metadata destinations that the Go-side guard
// would have rejected, including under DNS rebinding.
type GuardedProxy struct {
	guard  netguard.Guard
	ln     net.Listener
	addr   string
	client *http.Transport
	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

const (
	proxyIdleTimeout = 30 * time.Second
	proxyDialTimeout = 10 * time.Second
)

func NewGuardedProxy(guard netguard.Guard) (*GuardedProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           guard.DialContext,
		DisableKeepAlives:     false,
		ResponseHeaderTimeout: proxyDialTimeout,
		IdleConnTimeout:       proxyIdleTimeout,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
	}
	p := &GuardedProxy{guard: guard, ln: ln, addr: ln.Addr().String(), client: tr, conns: map[net.Conn]struct{}{}}
	go p.serve()
	return p, nil
}

// Addr returns the loopback listener address, e.g. "127.0.0.1:48231".
func (p *GuardedProxy) Addr() string { return p.addr }

func (p *GuardedProxy) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.ln.Close()
	p.client.CloseIdleConnections()
	return nil
}

func (p *GuardedProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = c.Close()
			return
		}
		p.conns[c] = struct{}{}
		p.mu.Unlock()
		go p.handle(c)
	}
}

func (p *GuardedProxy) handle(c net.Conn) {
	defer func() {
		p.mu.Lock()
		delete(p.conns, c)
		p.mu.Unlock()
		_ = c.Close()
	}()
	br := bufio.NewReader(c)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if req.Method == http.MethodConnect {
			p.handleConnect(c, req)
			return
		}
		if !p.handleHTTP(c, req) {
			return
		}
	}
}

// handleConnect validates and tunnels an HTTPS connection. The dial re-resolves
// the host through the guard, so a DNS rebinding that flips between the
// validation and connection is still blocked at dial time.
func (p *GuardedProxy) handleConnect(c net.Conn, req *http.Request) {
	host := req.Host
	if host == "" {
		p.refuse(c, http.StatusBadRequest, "missing host")
		return
	}
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		p.refuse(c, http.StatusBadRequest, "invalid host")
		return
	}
	if h == "" {
		p.refuse(c, http.StatusBadRequest, "missing hostname")
		return
	}
	if err := p.guard.ValidateHost(context.Background(), h); err != nil {
		p.refuse(c, http.StatusForbidden, "audit target is not a public address")
		return
	}
	upstream, err := p.guard.DialContext(context.Background(), "tcp", net.JoinHostPort(h, port))
	if err != nil {
		p.refuse(c, http.StatusBadGateway, "cannot reach audit target")
		return
	}
	defer upstream.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	p.tunnel(c, upstream)
}

func (p *GuardedProxy) tunnel(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copy := func(dst, src net.Conn, done chan<- struct{}) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			_ = src.SetReadDeadline(time.Now().Add(proxyIdleTimeout))
			n, rerr := src.Read(buf)
			if n > 0 {
				_ = dst.SetWriteDeadline(time.Now().Add(proxyIdleTimeout))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}
	go copy(a, b, done)
	go copy(b, a, done)
	<-done
	// Unblock the other direction once one side closes.
	_ = a.SetDeadline(time.Now())
	_ = b.SetDeadline(time.Now())
}

// handleHTTP validates and forwards a plain-HTTP (absolute-form) request. It
// does not follow redirects; Chromium follows them itself, and every redirect
// hop is a new proxied request that is validated again.
func (p *GuardedProxy) handleHTTP(c net.Conn, req *http.Request) bool {
	if req.URL == nil || !req.URL.IsAbs() {
		p.refuse(c, http.StatusBadRequest, "absolute URI required")
		return false
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		p.refuse(c, http.StatusForbidden, "scheme not allowed")
		return false
	}
	if err := p.guard.ValidateURL(context.Background(), req.URL); err != nil {
		p.refuse(c, http.StatusForbidden, "audit target is not a public address")
		return false
	}
	out := req.Clone(context.Background())
	out.RequestURI = ""
	out.Close = false
	resp, err := p.client.RoundTrip(out)
	if err != nil {
		p.refuse(c, http.StatusBadGateway, "cannot reach audit target")
		return false
	}
	defer resp.Body.Close()
	resp.Request = nil
	if err := resp.Write(c); err != nil {
		return false
	}
	return true
}

func (p *GuardedProxy) refuse(c net.Conn, status int, msg string) {
	resp := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(strings.TrimSpace(msg) + "\n")),
	}
	_ = resp.Write(c)
}
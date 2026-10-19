// Package pushgateway sends one-shot metric samples to a Prometheus
// Pushgateway over HTTP/1.1. The gateway expects the text exposition
// format — one "<metric> <value>" per line, terminated by '\n'.
//
// Endpoint convention:
//
//	POST  http://<host>:<port>/metrics/job/<job>/instance/<instance>
//	body  metric_name 1234\n
//
// The implementation uses raw net.Dial + a hand-written HTTP/1.1 request
// instead of net/http on purpose. TinyGo on ESP32-S3 runs over the lneto
// stack via espradio, and lneto's TCP socket pool is small. net/http kept
// recycled connections sitting in TIME_WAIT for tens of seconds and the
// pool would exhaust after a handful of POSTs (lneto.ErrExhausted).
// Driving a fresh dial-write-close cycle with "Connection: close" pushes
// the TIME_WAIT onto the server side, which is the same shape the
// tinygo-air-measurer project arrived at for the same reason.
package pushgateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const defaultTimeout = 10 * time.Second

// Pusher targets a single Pushgateway URL of the form
// "http://<host>:<port>/metrics/job/<job>/instance/<instance>".
type Pusher struct {
	URL string
	// Timeout bounds both connection setup and all request/response I/O.
	// Non-positive values use the 10-second default.
	Timeout time.Duration
	// Trace receives diagnostic stage names immediately before and after
	// potentially blocking network operations. It may be nil.
	Trace func(string)

	addr string
	path string
	host string
}

// Async runs at most one Push in a background goroutine. This keeps a broken
// network dial from blocking sensor sampling on TinyGo targets whose net
// package does not enforce DialTimeout.
type Async struct {
	push    func(string) error
	result  chan error
	started time.Time
}

// NewAsync wraps push with single-flight asynchronous execution.
func NewAsync(push func(string) error) *Async { return &Async{push: push} }

// Start begins a push unless one is already in flight.
func (a *Async) Start(body string) bool {
	if a == nil || a.push == nil || a.result != nil {
		return false
	}
	result := make(chan error, 1)
	a.result = result
	a.started = time.Now()
	go func() {
		result <- a.push(body)
	}()
	return true
}

// Poll reports a completed push. done is false while idle or still running.
func (a *Async) Poll() (done bool, err error) {
	if a == nil || a.result == nil {
		return false, nil
	}
	select {
	case err := <-a.result:
		a.result = nil
		a.started = time.Time{}
		return true, err
	default:
		return false, nil
	}
}

// Stalled reports whether the current push has exceeded timeout.
func (a *Async) Stalled(now time.Time, timeout time.Duration) bool {
	return a != nil && a.result != nil && timeout > 0 && now.Sub(a.started) >= timeout
}

func (p *Pusher) trace(stage string) {
	if p.Trace != nil {
		p.Trace(stage)
	}
}

// New constructs a Pusher and pre-parses the URL into host/path components
// so Push doesn't have to reparse on every call. Returns an empty-URL Pusher
// if url cannot be split — Push will then return an error on each call.
func New(url string) *Pusher {
	p := &Pusher{URL: url, Timeout: defaultTimeout}
	if !strings.HasPrefix(url, "http://") {
		return p
	}
	rest := strings.TrimPrefix(url, "http://")
	slash := strings.Index(rest, "/")
	if slash < 0 {
		p.addr = rest
		p.path = "/"
	} else {
		p.addr = rest[:slash]
		p.path = rest[slash:]
	}
	if colon := strings.LastIndex(p.addr, ":"); colon >= 0 {
		p.host = p.addr[:colon]
	} else {
		p.host = p.addr
	}
	return p
}

// Push opens a fresh TCP connection, writes a single HTTP/1.1 POST with
// "Connection: close", drains the response, and closes the socket. Errors
// from any of those steps come back to the caller verbatim — including
// lneto.ErrExhausted, which surfaces as "resource exhausted" and means the
// host stack ran out of sockets and the push needs to be tried later.
func (p *Pusher) Push(body string) error {
	if p == nil || p.URL == "" || p.addr == "" {
		return fmt.Errorf("pushgateway: URL not set")
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	p.trace("dial:start")
	conn, err := net.DialTimeout("tcp", p.addr, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	p.trace("dial:done")
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	p.trace("write:start")

	w := bufio.NewWriter(conn)
	fmt.Fprintf(w, "POST %s HTTP/1.1\r\n", p.path)
	fmt.Fprintf(w, "Host: %s\r\n", p.host)
	fmt.Fprintf(w, "User-Agent: TinyGo\r\n")
	fmt.Fprintf(w, "Connection: close\r\n")
	fmt.Fprintf(w, "Content-Type: text/plain\r\n")
	fmt.Fprintf(w, "Content-Length: %d\r\n", len(body))
	fmt.Fprintf(w, "\r\n")
	w.WriteString(body)
	if err := w.Flush(); err != nil {
		return err
	}
	p.trace("write:done")

	r := bufio.NewReader(conn)
	p.trace("status:start")
	status, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	p.trace("status:done")
	// Drain the rest of the response so the server can finish its FIN and
	// our socket can fully close — without this, lneto sometimes keeps the
	// half-closed socket around long enough to starve subsequent pushes.
	p.trace("drain:start")
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	p.trace("drain:done")

	status = strings.TrimSpace(status)
	if !strings.HasPrefix(status, "HTTP/1.1 2") && !strings.HasPrefix(status, "HTTP/1.0 2") {
		return fmt.Errorf("pushgateway: %s", status)
	}
	p.trace("done")
	return nil
}

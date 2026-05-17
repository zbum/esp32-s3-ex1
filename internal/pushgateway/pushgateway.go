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
)

// Pusher targets a single Pushgateway URL of the form
// "http://<host>:<port>/metrics/job/<job>/instance/<instance>".
type Pusher struct {
	URL string

	addr string
	path string
	host string
}

// New constructs a Pusher and pre-parses the URL into host/path components
// so Push doesn't have to reparse on every call. Returns an empty-URL Pusher
// if url cannot be split — Push will then return an error on each call.
func New(url string) *Pusher {
	p := &Pusher{URL: url}
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

	conn, err := net.Dial("tcp", p.addr)
	if err != nil {
		return err
	}
	defer conn.Close()

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

	r := bufio.NewReader(conn)
	status, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	// Drain the rest of the response so the server can finish its FIN and
	// our socket can fully close — without this, lneto sometimes keeps the
	// half-closed socket around long enough to starve subsequent pushes.
	_, _ = io.Copy(io.Discard, r)

	status = strings.TrimSpace(status)
	if !strings.HasPrefix(status, "HTTP/1.1 2") && !strings.HasPrefix(status, "HTTP/1.0 2") {
		return fmt.Errorf("pushgateway: %s", status)
	}
	return nil
}

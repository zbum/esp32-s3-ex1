// Package pushgateway sends one-shot metric samples to a Prometheus
// Pushgateway over HTTP. The gateway expects the standard text exposition
// format — one "<metric> <value>" per line, terminated by '\n'.
//
// Endpoint convention:
//
//	POST  http://<host>:<port>/metrics/job/<job>/instance/<instance>
//	body  metric_name 1234\n
//
// This package does not retain metric state; each Push is a complete write
// that replaces all samples for the (job, instance) pair on the gateway.
package pushgateway

import (
	"fmt"
	"net/http"
	"strings"
)

// Pusher targets a single Pushgateway URL. The URL is the full path,
// including job and instance segments, that the gateway listens at.
type Pusher struct {
	URL string
}

// New constructs a Pusher. If url is empty the resulting Pusher returns an
// error on every Push so callers can still build a struct without a network
// available.
func New(url string) *Pusher {
	return &Pusher{URL: url}
}

// Push posts a Prometheus text-exposition body. A trailing '\n' is appended
// if missing — the gateway rejects bodies without it.
func (p *Pusher) Push(body string) error {
	if p == nil || p.URL == "" {
		return fmt.Errorf("pushgateway: URL not set")
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	resp, err := http.Post(p.URL, "text/plain", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pushgateway: status %d", resp.StatusCode)
	}
	return nil
}

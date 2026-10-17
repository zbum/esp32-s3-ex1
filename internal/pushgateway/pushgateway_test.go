package pushgateway

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPushSuccess(t *testing.T) {
	url := serveOnce(t, func(conn net.Conn) {
		readRequest(t, conn)
		fmt.Fprint(conn, "HTTP/1.1 202 Accepted\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	})

	if err := New(url).Push("sample_metric 1"); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
}

func TestPushTimesOutWhenResponseDoesNotFinish(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	url := serveOnce(t, func(conn net.Conn) {
		readRequest(t, conn)
		fmt.Fprint(conn, "HTTP/1.1 202 Accepted\r\nContent-Length: 1\r\n\r\n")
		<-release
	})
	pusher := New(url)
	pusher.Timeout = 50 * time.Millisecond

	if err := pusher.Push("sample_metric 1"); err == nil {
		t.Fatal("Push() error = nil, want timeout")
	}
}

func serveOnce(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn)
	}()
	return "http://" + listener.Addr().String() + "/metrics/job/test"
}

func readRequest(t *testing.T, conn net.Conn) {
	t.Helper()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Errorf("ReadString() error = %v", err)
			return
		}
		if strings.TrimSpace(line) == "" {
			return
		}
	}
}

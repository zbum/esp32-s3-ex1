package pushgateway

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAsyncCompletesWithoutBlockingCaller(t *testing.T) {
	wantErr := errors.New("push failed")
	started := make(chan struct{})
	release := make(chan struct{})

	a := NewAsync(func(string) error {
		close(started)
		<-release
		return wantErr
	})
	if !a.Start("metric 1") {
		t.Fatal("Start returned false")
	}
	<-started
	if a.Start("metric 2") {
		t.Fatal("second Start should be rejected while push is running")
	}
	if done, _ := a.Poll(); done {
		t.Fatal("Poll completed before worker was released")
	}
	close(release)
	for {
		done, err := a.Poll()
		if !done {
			time.Sleep(time.Millisecond)
			continue
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("Poll error = %v, want %v", err, wantErr)
		}
		break
	}
}

func TestAsyncDetectsStalledPush(t *testing.T) {
	a := NewAsync(nil)
	a.result = make(chan error, 1)
	a.started = time.Unix(100, 0)
	if a.Stalled(time.Unix(109, 0), 10*time.Second) {
		t.Fatal("reported stalled before timeout")
	}
	if !a.Stalled(time.Unix(110, 0), 10*time.Second) {
		t.Fatal("did not report stalled at timeout")
	}
}

func TestPushSuccess(t *testing.T) {
	url := serveOnce(t, func(conn net.Conn) {
		readRequest(t, conn)
		fmt.Fprint(conn, "HTTP/1.1 202 Accepted\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	})

	if err := New(url).Push("sample_metric 1"); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
}

func TestPushTracesNetworkStages(t *testing.T) {
	url := serveOnce(t, func(conn net.Conn) {
		readRequest(t, conn)
		fmt.Fprint(conn, "HTTP/1.1 202 Accepted\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	})
	pusher := New(url)
	var stages []string
	pusher.Trace = func(stage string) {
		stages = append(stages, stage)
	}

	if err := pusher.Push("sample_metric 1"); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	want := "dial:start,dial:done,write:start,write:done,status:start,status:done,drain:start,drain:done,done"
	if got := strings.Join(stages, ","); got != want {
		t.Fatalf("trace stages = %q, want %q", got, want)
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

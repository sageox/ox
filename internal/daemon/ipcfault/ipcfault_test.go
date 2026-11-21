package ipcfault

import (
	"bufio"
	"net"
	"testing"
	"time"
)

func roundTrip(t *testing.T, path, line string) (string, error) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	return bufio.NewReader(conn).ReadString('\n')
}

func echo(req []byte) []byte { return append([]byte("ok:"), req[:len(req)-1]...) }

func TestHealthyServerRepliesAndCountsServed(t *testing.T) {
	t.Parallel()
	s := New(t, echo)
	got, err := roundTrip(t, s.Path(), "ping")
	if err != nil || got != "ok:ping\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	s.WaitServed(1)
}

func TestCloseMidResponseRunsHandlerButSendsNothing(t *testing.T) {
	t.Parallel()
	handled := make(chan struct{}, 1)
	s := New(t, func(req []byte) []byte { handled <- struct{}{}; return echo(req) })
	s.Inject(Fault{CloseMidResponse: true})
	if got, err := roundTrip(t, s.Path(), "ping"); err == nil {
		t.Fatalf("expected EOF, got reply %q", got)
	}
	s.WaitServed(1)
	select {
	case <-handled:
	default:
		t.Fatal("handler did not run: the work must happen before the response is lost")
	}
	// the fault is consumed; the next request is healthy
	if got, err := roundTrip(t, s.Path(), "again"); err != nil || got != "ok:again\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestHoldBlocksUntilReleased(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	s := New(t, echo)
	s.Inject(Fault{Hold: hold})
	res := make(chan string, 1)
	go func() { got, _ := roundTrip(t, s.Path(), "slow"); res <- got }()
	select {
	case got := <-res:
		t.Fatalf("replied while held: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(hold)
	if got := <-res; got != "ok:slow\n" {
		t.Fatalf("got %q", got)
	}
}

func TestClientHangupBeforeRequestLineStillCountsServed(t *testing.T) {
	t.Parallel()
	s := New(t, echo)
	conn, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	s.WaitServed(1)
}

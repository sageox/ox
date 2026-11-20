// Package ipcfault is the shared IPC test transport: a unix-socket server that
// speaks the daemon's newline-delimited request/response framing and can fail
// the way a loaded daemon does. A test wires a handler (the real code under
// test) behind it and drives it with the real daemon.Client, so the faults a
// hook meets (a read that times out, a connection dropped, a response lost
// after the work was done) are exercised end to end instead of being stubbed
// away. It imports nothing from the daemon, so in-package tests can use it.
package ipcfault

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Fault is what the next request meets. The zero value is a healthy daemon.
type Fault struct {
	// Delay holds the request before it is handled, as a daemon stuck behind a
	// lock does; a client whose read timeout is shorter gives up and goes away.
	// The request is still handled afterwards: the daemon cannot know.
	Delay time.Duration
	// Drop closes the connection without handling the request.
	Drop bool
	// CloseMidResponse handles the request, then closes the connection without
	// sending the response: the work happened, the caller never hears.
	CloseMidResponse bool
}

// Server is the fake daemon.
type Server struct {
	path    string
	handler func(request []byte) []byte
	ln      net.Listener

	mu       sync.Mutex
	faults   []Fault
	requests int
	wg       sync.WaitGroup // the accept loop
	reqs     sync.WaitGroup // requests being served
}

// New starts a server answering every request line with handler's reply line.
// The socket lives in a short temp path (unix socket paths are length-limited).
func New(t testing.TB, handler func(request []byte) []byte) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "oxf")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{path: filepath.Join(dir, "d.sock"), handler: handler}
	if s.ln, err = net.Listen("unix", s.path); err != nil {
		t.Fatal(err)
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(func() {
		_ = s.ln.Close()
		s.wg.Wait()
		s.reqs.Wait()
		_ = os.RemoveAll(dir)
	})
	return s
}

// Path is the socket path for a client.
func (s *Server) Path() string { return s.path }

// Inject queues faults; each request consumes the next one, in order.
func (s *Server) Inject(faults ...Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, faults...)
}

// Requests counts the requests that reached the server.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Wait blocks until every in-flight request has been handled, including ones
// whose client already gave up.
func (s *Server) Wait() { s.reqs.Wait() }

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.reqs.Add(1)
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer s.reqs.Done()
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	s.mu.Lock()
	s.requests++
	var f Fault
	if len(s.faults) > 0 {
		f, s.faults = s.faults[0], s.faults[1:]
	}
	s.mu.Unlock()
	if f.Drop {
		return
	}
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
	reply := s.handler(line)
	if f.CloseMidResponse {
		return
	}
	_, _ = conn.Write(append(reply, '\n'))
}

package extmsg

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// ErrInUse means another live editor already listens on the socket — the
// same project is open twice, and only the first instance takes messages.
var ErrInUse = errors.New("another ntee is already listening for this project")

// Handler turns one received request (the raw text) into its reply line. It
// runs on the connection's goroutine, never the UI's.
type Handler func(text string) string

// Server accepts requests on a unix socket.
type Server struct {
	listener        net.Listener
	path            string
	closed          atomic.Bool
	connectionSlots chan struct{}
}

// maxConnections caps the requests being served at once — a wait holds its
// connection until the user is done — so a misbehaving sender can't pile up
// goroutines and file descriptors. A var so tests can lower it.
var maxConnections = 128

// busyReply answers a connection past maxConnections.
var busyReply = ErrorReply("the editor is busy — try again shortly")

// staleDialTimeout bounds the probe that tells a live socket from a leftover
// file of a crashed instance.
const staleDialTimeout = 300 * time.Millisecond

// Listen starts accepting requests on socketPath. The socket's directory is
// private to the user (0700) and the socket itself 0600, so only the user's
// own processes can talk to the editor. A leftover socket file with nobody
// listening is replaced; a live one returns ErrInUse.
func Listen(socketPath string, handler Handler) (*Server, error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(socketPath); err == nil {
		if conn, dialErr := net.DialTimeout("unix", socketPath, staleDialTimeout); dialErr == nil {
			conn.Close()
			return nil, ErrInUse
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	server := &Server{listener: listener, path: socketPath, connectionSlots: make(chan struct{}, maxConnections)}
	go server.acceptLoop(handler)
	return server, nil
}

// Path is the socket the server listens on.
func (s *Server) Path() string {
	return s.path
}

func (s *Server) acceptLoop(handler Handler) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			time.Sleep(50 * time.Millisecond) // transient (fd exhaustion): don't spin
			continue
		}
		select {
		case s.connectionSlots <- struct{}{}:
			go func() {
				defer func() { <-s.connectionSlots }()
				serve(conn, handler)
			}()
		default:
			// Still read the request (bounded by the read deadline) so the
			// sender sees the reply instead of a reset.
			go serve(conn, func(string) string { return busyReply })
		}
	}
}

// serve reads one request — the sender half-closes when done — and writes
// the handler's reply.
func serve(conn net.Conn, handler Handler) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(ReadTimeout))
	data, err := io.ReadAll(io.LimitReader(conn, MaxMessageBytes+1))
	var reply string
	switch {
	case len(data) == 0 && err == nil:
		return // a liveness probe (Discover, a second instance): connect, close
	case len(data) > MaxMessageBytes:
		// Drain the rest (still under the read deadline) before answering:
		// closing with unread input would fail the sender's in-flight write
		// and it would never see this reply.
		_, _ = io.Copy(io.Discard, conn)
		reply = ErrorReply("message too large")
	case err != nil:
		reply = ErrorReply("message not completed within the read timeout — close the write side when done")
	default:
		reply = handler(string(data))
	}
	_ = conn.SetWriteDeadline(time.Now().Add(ReadTimeout))
	_, _ = io.WriteString(conn, reply+"\n")
}

// Close stops accepting and removes the socket file. Requests already being
// served finish on their own goroutines.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := s.listener.Close()
	// net's unix listener unlinks its socket file on Close; this covers the
	// platforms and edge cases where it doesn't.
	if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
		err = removeErr
	}
	return err
}

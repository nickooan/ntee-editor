package extmsg

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoEditor means no running editor was found for the directory.
var ErrNoEditor = errors.New("no running ntee found for this directory — open the project in ntee first")

// ErrNoReply means the editor closed the connection without answering —
// most likely it quit or restarted while the request was held (a wait).
var ErrNoReply = errors.New("the editor closed the connection without answering — it may have quit or restarted")

const (
	dialTimeout  = 2 * time.Second
	replyTimeout = 10 * time.Second
)

// Send delivers one request and returns the editor's reply line.
func Send(socketPath, text string) (string, error) {
	return SendWithTimeout(socketPath, text, replyTimeout)
}

// SendWithTimeout is Send with the time to wait for the reply — a session
// step waits for the user, far longer than a plain request. A timeout is an
// error satisfying os.IsTimeout.
func SendWithTimeout(socketPath, text string, timeout time.Duration) (string, error) {
	reply, err := exchange(context.Background(), socketPath, text, timeout, replyLimit)
	if err != nil {
		return "", err
	}
	return trimReply(reply), nil
}

// SendForBody is SendWithTimeout for a request whose reply may carry a body
// (BodyReply): it reads up to MaxBodyBytes and keeps the body byte for
// byte. line is the reply's first line.
func SendForBody(socketPath, text string, timeout time.Duration) (line, body string, hasBody bool, err error) {
	reply, err := exchange(context.Background(), socketPath, text, timeout, MaxBodyBytes+64)
	if err != nil {
		return "", "", false, err
	}
	line, body, hasBody = SplitBodyReply(string(reply))
	return line, body, hasBody, nil
}

// replyLimit caps a one-line reply.
const replyLimit = 64 << 10

// exchange sends one request and reads the raw reply, up to limit bytes.
// Cancelling ctx closes the connection.
func exchange(ctx context.Context, socketPath, text string, timeout time.Duration, limit int64) ([]byte, error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stopClosing := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClosing()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	writeErr := writeRequest(conn, text)
	reply, readErr := io.ReadAll(io.LimitReader(conn, limit))
	// The editor may answer and close before reading everything (a refused
	// oversize request): the write then fails, but its reply is still there.
	if strings.TrimSpace(string(reply)) != "" {
		return reply, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if writeErr != nil {
		return nil, writeErr
	}
	if readErr == nil {
		readErr = ErrNoReply // the editor always answers a request with a line
	}
	return nil, readErr
}

func trimReply(reply []byte) string {
	return strings.TrimSpace(string(reply))
}

// writeRequest sends the request and half-closes — the end-of-request marker
// the server reads up to.
func writeRequest(conn net.Conn, text string) error {
	if _, err := io.WriteString(conn, text); err != nil {
		return err
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		return unixConn.CloseWrite()
	}
	return nil
}

// Discover finds the editor serving startDir: the nearest ancestor (startDir
// included) that is the opened root of a running editor. socketFor maps a
// project root to its socket path. It returns that root and its socket.
func Discover(startDir string, socketFor func(projectRoot string) (string, error)) (projectRoot, socketPath string, err error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", "", err
	}
	for {
		if path, pathErr := socketFor(dir); pathErr == nil && isLive(path) {
			return dir, path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", ErrNoEditor
		}
		dir = parent
	}
}

func isLive(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, staleDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

package extmsg

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoEditor means no running editor was found for the directory.
var ErrNoEditor = errors.New("no running ntee found for this directory — open the project in ntee first")

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
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	writeErr := writeRequest(conn, text)
	reply, readErr := io.ReadAll(io.LimitReader(conn, 64<<10))
	// The editor may answer and close before reading everything (a refused
	// oversize request): the write then fails, but its reply is still there.
	if line := strings.TrimSpace(string(reply)); line != "" {
		return line, nil
	}
	if writeErr != nil {
		return "", writeErr
	}
	return "", readErr
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

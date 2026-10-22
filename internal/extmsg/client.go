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
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(replyTimeout))
	if _, err := io.WriteString(conn, text); err != nil {
		return "", err
	}
	// The half-close is the end-of-request marker the server reads up to.
	if unixConn, ok := conn.(*net.UnixConn); ok {
		if err := unixConn.CloseWrite(); err != nil {
			return "", err
		}
	}
	reply, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(reply)), nil
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

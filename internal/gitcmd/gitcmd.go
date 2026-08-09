// Package gitcmd is the one place the editor spawns git. Every call runs a
// short-lived child with a hard deadline, so a wedged git (NFS root,
// index.lock contention, a hung fsmonitor hook) can never hang a worker
// goroutine — or the UI waiting on one — indefinitely.
package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Timeout bounds one git invocation. Generous against the worst legitimate
// cases (status/blame --porcelain on huge repos); a var so tests can shrink it.
var Timeout = 10 * time.Second

// Out runs git with the given args against root and returns stdout; stderr
// rides the *exec.ExitError for error reporting.
func Out(root string, args ...string) ([]byte, error) {
	return runIn(root, nil, args...)
}

// OutIn is Out with the given bytes on the child's stdin (for
// blame --contents=-, which reads the buffer content from there).
func OutIn(root string, stdin []byte, args ...string) ([]byte, error) {
	return runIn(root, bytes.NewReader(stdin), args...)
}

func runIn(root string, stdin io.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Stdin = stdin
	cmd.WaitDelay = 2 * time.Second // reclaim the pipes even if a grandchild inherits them
	out, err := cmd.Output()
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: timed out after %s", args[0], Timeout)
	}
	return out, err
}

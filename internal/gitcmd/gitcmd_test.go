package gitcmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// initRepo builds a throwaway repo with one committed file.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test")
	run("config", "user.name", "test")
	return root
}

func TestOutRunsGit(t *testing.T) {
	root := initRepo(t)
	out, err := Out(root, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("clean repo status = %q", out)
	}
}

func TestOutInFeedsStdin(t *testing.T) {
	root := initRepo(t)
	out, err := OutIn(root, []byte("hello\n"), "hash-object", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(out))) != 40 {
		t.Fatalf("hash-object output = %q", out)
	}
}

func TestTimeoutKillsHungGit(t *testing.T) {
	root := initRepo(t)
	old := Timeout
	Timeout = 100 * time.Millisecond
	defer func() { Timeout = old }()

	// An os.Pipe with no writer activity wedges `hash-object --stdin` until
	// the context deadline kills the child. (An *os.File stdin is handed to
	// the child as a real fd — no exec copy goroutine that a plain blocked
	// Reader would strand.)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	start := time.Now()
	_, err = runIn(root, pr, "hash-object", "--stdin")
	if err == nil {
		t.Fatal("hung git should error out")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timed-out error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s, timeout not enforced", elapsed)
	}
}

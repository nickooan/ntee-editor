// Package clipboard copies text to the system clipboard on macOS and Linux. It
// prefers each platform's native clipboard CLI (reliable locally, including
// terminals like Apple Terminal that do not support OSC 52) and falls back to
// an OSC 52 escape written to the controlling terminal for SSH/tmux/headless
// sessions where no clipboard tool is available.
package clipboard

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// copyTimeout bounds each native clipboard tool run: a hung wl-copy (no
// compositor) or a tool holding the selection must not freeze the caller —
// the same hard-deadline rule gitcmd applies to git spawns.
const copyTimeout = 3 * time.Second

// clipCmd is one native clipboard command candidate.
type clipCmd struct {
	name string
	args []string
}

// nativeCommands returns the ordered clipboard CLIs to try for the current OS.
func nativeCommands() []clipCmd {
	switch runtime.GOOS {
	case "darwin":
		return []clipCmd{{name: "pbcopy"}}
	case "linux":
		return []clipCmd{
			{name: "wl-copy"}, // Wayland
			{name: "xclip", args: []string{"-selection", "clipboard"}}, // X11
			{name: "xsel", args: []string{"--clipboard", "--input"}},   // X11 (alt)
		}
	}
	return nil
}

// Copy places s on the system clipboard, trying the native CLI first and
// falling back to OSC 52.
func Copy(s string) error {
	for _, c := range nativeCommands() {
		path, err := exec.LookPath(c.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
		cmd := exec.CommandContext(ctx, path, c.args...)
		cmd.WaitDelay = time.Second // reclaim the process even if it ignores the kill signal's pipe close
		cmd.Stdin = strings.NewReader(s)
		err = cmd.Run()
		cancel()
		if err == nil {
			return nil
		}
		// Tool present but failed (or timed out) — try the next candidate,
		// then OSC 52.
	}
	return writeOSC52(s)
}

// osc52Seq builds the OSC 52 clipboard-set escape sequence for s.
func osc52Seq(s string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\a"
}

// writeOSC52 emits the OSC 52 sequence to /dev/tty so it reaches the terminal
// out-of-band without disturbing the alt-screen render on stdout.
func writeOSC52(s string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	_, err = io.WriteString(tty, osc52Seq(s))
	return err
}

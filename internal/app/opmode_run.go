package app

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	opRunEventBuffer  = 64
	opRunReadSize     = 32 << 10
	opRunCoalesceSize = 64 << 10 // bytes folded into one opRunMsg (one frame)
	opRunMaxLines     = 5000
	opRunKillGrace    = 2 * time.Second
)

// opRunState is one op-command execution. The process and its pipe reader
// live on background goroutines that only ever touch events; every other field
// is owned by the UI goroutine.
type opRunState struct {
	commandLine string
	dir         string
	ctx         context.Context
	cancel      context.CancelFunc
	events      chan opRunEvent // closed after the final (done) event

	output   opRunOutput
	scroll   int // lines scrolled up from the tail; 0 follows new output
	finished bool
	exitCode int
	err      error
}

type opRunEvent struct {
	data     []byte
	done     bool
	exitCode int
	err      error
}

// opRunMsg carries coalesced output, and on the last message the exit status.
type opRunMsg struct {
	gen int
	opRunEvent
}

func newOpRun(commandLine, dir string) *opRunState {
	ctx, cancel := context.WithCancel(context.Background())
	return &opRunState{
		commandLine: commandLine,
		dir:         dir,
		ctx:         ctx,
		cancel:      cancel,
		events:      make(chan opRunEvent, opRunEventBuffer),
	}
}

// startOpRunCmd launches the process off the UI goroutine and returns its
// first output (or completion) as a message.
func startOpRunCmd(gen int, run *opRunState) tea.Cmd {
	return func() tea.Msg {
		go run.execute()
		return run.next(gen)
	}
}

func waitOpRunCmd(gen int, run *opRunState) tea.Cmd {
	return func() tea.Msg { return run.next(gen) }
}

// next blocks for one event, then folds whatever else is already queued into
// the same message so a chatty process costs few frames.
func (run *opRunState) next(gen int) tea.Msg {
	event, ok := <-run.events
	if !ok {
		return opRunMsg{gen: gen, opRunEvent: opRunEvent{done: true, exitCode: -1, err: context.Canceled}}
	}
coalesce:
	for !event.done && len(event.data) < opRunCoalesceSize {
		select {
		case more, ok := <-run.events:
			if !ok {
				break coalesce
			}
			event.data = append(event.data, more.data...)
			event.done, event.exitCode, event.err = more.done, more.exitCode, more.err
		default:
			break coalesce
		}
	}
	return opRunMsg{gen: gen, opRunEvent: event}
}

// send delivers an event unless the run was cancelled — after Esc nobody
// drains events, and a blocked send would leak the goroutine.
func (run *opRunState) send(event opRunEvent) {
	select {
	case run.events <- event:
	case <-run.ctx.Done():
	}
}

// execute runs `sh -c commandLine` in its own process group (so cancelling
// also kills the shell's children), streaming merged stdout/stderr.
func (run *opRunState) execute() {
	defer close(run.events)
	command := exec.CommandContext(run.ctx, "sh", "-c", run.commandLine)
	command.Dir = run.dir
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
	// Bounds Wait when a SIGTERM-ignoring child keeps the pipe open.
	command.WaitDelay = opRunKillGrace
	pipeReader, pipeWriter := io.Pipe()
	command.Stdout, command.Stderr = pipeWriter, pipeWriter

	if err := command.Start(); err != nil {
		run.send(opRunEvent{done: true, exitCode: -1, err: err})
		return
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buffer := make([]byte, opRunReadSize)
		for {
			count, err := pipeReader.Read(buffer)
			if count > 0 {
				run.send(opRunEvent{data: append([]byte(nil), buffer[:count]...)})
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := command.Wait()
	_ = pipeWriter.Close()
	<-readerDone

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case run.ctx.Err() != nil:
		exitCode, waitErr = -1, context.Canceled
	case errors.As(waitErr, &exitErr):
		exitCode, waitErr = exitErr.ExitCode(), nil
	case waitErr != nil:
		exitCode = -1
	}
	run.send(opRunEvent{done: true, exitCode: exitCode, err: waitErr})
}

// opRunOutput accumulates terminal output as display lines: ANSI escapes are
// stripped, CRLF is a newline, and a bare CR rewinds the current line so
// progress bars redraw in place instead of piling up.
type opRunOutput struct {
	lines     []string
	current   strings.Builder
	pendingCR bool // a chunk ended in \r: the next byte decides CRLF vs rewind
}

func (output *opRunOutput) write(data []byte) {
	text := ansi.Strip(string(data))
	for len(text) > 0 {
		if output.pendingCR {
			output.pendingCR = false
			if text[0] == '\n' {
				output.pushLine()
				text = text[1:]
				continue
			}
			output.current.Reset()
		}
		cut := strings.IndexAny(text, "\r\n")
		if cut < 0 {
			output.current.WriteString(text)
			return
		}
		output.current.WriteString(text[:cut])
		if text[cut] == '\n' {
			output.pushLine()
		} else {
			output.pendingCR = true
		}
		text = text[cut+1:]
	}
}

func (output *opRunOutput) pushLine() {
	output.lines = append(output.lines, output.current.String())
	output.current.Reset()
	// Trim in batches so the copy cost amortizes across many lines.
	if len(output.lines) > opRunMaxLines+opRunMaxLines/4 {
		output.lines = append([]string(nil), output.lines[len(output.lines)-opRunMaxLines:]...)
	}
}

// displayLines is the completed lines plus the in-progress partial one.
func (output *opRunOutput) displayLines() []string {
	if output.current.Len() == 0 {
		return output.lines
	}
	return append(output.lines[:len(output.lines):len(output.lines)], output.current.String())
}

// Package mcpserver serves the editor's agent edit sessions as MCP tools
// over stdio: each tool call becomes one request on the running editor's
// unix socket (internal/extmsg).
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Config is what the server needs from its host.
type Config struct {
	// FindSocket finds the running editor's socket for the named absolute
	// paths (none for session-wide commands).
	FindSocket func(paths []string) (string, error)
	// WorkingDir resolves relative tool paths; empty means the process's.
	WorkingDir string
	// Version is reported in serverInfo.
	Version string
	// Now stamps generated session ids; nil means time.Now.
	Now func() time.Time
	// RequestTimeout bounds a staging request; zero means 10s.
	RequestTimeout time.Duration
	// EndTimeout bounds end, which may write every staged file; zero means 45s.
	EndTimeout time.Duration
}

const (
	defaultProtocolVersion = "2025-06-18"
	// maxLineBytes caps one JSON-RPC message: a 1 MiB payload escaped as
	// JSON stays well under it.
	maxLineBytes = 16 << 20

	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

var knownProtocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type server struct {
	config Config
	tools  *editTools

	writeMu sync.Mutex
	output  *bufio.Writer

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc
	calls      sync.WaitGroup
}

// Serve answers MCP requests read line by line from input, writing one
// JSON-RPC message per line to output, until input ends or ctx is done
// (which also stops the calls still running). Tool calls run concurrently,
// so a long edit_wait never holds up others.
func Serve(ctx context.Context, input io.Reader, output io.Writer, config Config) error {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 10 * time.Second
	}
	if config.EndTimeout == 0 {
		config.EndTimeout = 45 * time.Second
	}
	if config.WorkingDir == "" {
		workingDir, err := os.Getwd()
		if err != nil {
			return err
		}
		config.WorkingDir = workingDir
	}
	s := &server{config: config, tools: newEditTools(config), output: bufio.NewWriter(output), inflight: map[string]context.CancelFunc{}}
	// When input ends, calls still running get to answer: a client closes
	// stdin first and signals the process only if it does not exit.
	defer s.calls.Wait()

	reader := bufio.NewReaderSize(input, 64<<10)
	for {
		line, tooLong, err := readLine(reader)
		if tooLong {
			s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeParseError, Message: "message too large"})
		} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			s.dispatch(ctx, trimmed)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// readLine reads one line; past maxLineBytes the rest of it is drained and
// tooLong is set.
func readLine(reader *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, readErr := reader.ReadLine()
		if !tooLong {
			if len(line)+len(chunk) > maxLineBytes {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if readErr != nil || !isPrefix {
			return line, tooLong, readErr
		}
	}
}

func (s *server) dispatch(ctx context.Context, data []byte) {
	var request rpcRequest
	if err := json.Unmarshal(data, &request); err != nil {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()})
		return
	}
	isNotification := len(request.ID) == 0
	if isNotification {
		if request.Method == "notifications/cancelled" {
			s.cancel(request.Params)
		}
		return
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		s.reply(request.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "invalid request: needs jsonrpc \"2.0\" and a method"})
		return
	}
	switch request.Method {
	case "initialize":
		s.reply(request.ID, s.initializeResult(request.Params), nil)
	case "ping":
		s.reply(request.ID, map[string]any{}, nil)
	case "tools/list":
		s.reply(request.ID, map[string]any{"tools": toolDefinitions}, nil)
	case "tools/call":
		s.startCall(ctx, request)
	default:
		s.reply(request.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + request.Method})
	}
}

func (s *server) initializeResult(params json.RawMessage) map[string]any {
	var asked struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &asked)
	version := defaultProtocolVersion
	for _, known := range knownProtocolVersions {
		if asked.ProtocolVersion == known {
			version = known
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "ntee", "version": s.config.Version},
		"instructions":    instructions,
	}
}

// startCall runs one tools/call on its own goroutine. A call cancelled by
// the client gets no response, as MCP asks.
func (s *server) startCall(ctx context.Context, request rpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
		s.reply(request.ID, nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a tool name"})
		return
	}
	if !knownTool(params.Name) {
		s.reply(request.ID, nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + params.Name})
		return
	}
	callCtx, cancel := context.WithCancel(ctx)
	key := string(request.ID)
	s.inflightMu.Lock()
	s.inflight[key] = cancel
	s.inflightMu.Unlock()
	s.calls.Add(1)
	go func() {
		defer s.calls.Done()
		defer func() {
			s.inflightMu.Lock()
			delete(s.inflight, key)
			s.inflightMu.Unlock()
			cancel()
		}()
		result := s.tools.call(callCtx, params.Name, params.Arguments)
		if callCtx.Err() != nil {
			return
		}
		s.reply(request.ID, result, nil)
	}()
}

func (s *server) cancel(params json.RawMessage) {
	var cancelled struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &cancelled) != nil || len(cancelled.RequestID) == 0 {
		return
	}
	s.inflightMu.Lock()
	cancel := s.inflight[strings.TrimSpace(string(cancelled.RequestID))]
	s.inflightMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *server) reply(id json.RawMessage, result any, rpcErr *rpcError) {
	data, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
	if err != nil {
		data, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32603, Message: err.Error()}})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.output.Write(append(data, '\n'))
	_ = s.output.Flush()
}

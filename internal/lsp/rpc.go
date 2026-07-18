package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Hand-rolled, dependency-free JSON-RPC 2.0 endpoint over LSP-style
// Content-Length framing (ported from ntee-r1quest's jsonrpc package, with
// the pending map keyed by raw id bytes so string ids round-trip too).

// Message is a single JSON-RPC frame. The kind is inferred from which fields
// are present: request = Method+ID, notification = Method only, response =
// ID + Result/Error. ID is raw so numbers and strings round-trip unchanged.
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

const (
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

func (m *Message) isResponse() bool { return m.Method == "" }

// Handler handles one inbound request or notification. Return a value to
// answer a request; the return value is ignored for notifications.
type Handler func(method string, params json.RawMessage) (any, error)

// Conn is a full-duplex JSON-RPC 2.0 endpoint over any io.ReadWriteCloser
// (a child process stdio pair, or an in-memory pipe in tests).
type Conn struct {
	rw      io.ReadWriteCloser
	reader  *bufio.Reader
	handler Handler

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan *Message
	closed  bool
	closeFn sync.Once

	// Notifications are dispatched by a single worker so they stay in
	// arrival order (publishDiagnostics must not be reordered).
	notifications chan *Message
	done          chan struct{}
}

// NewConn starts a connection and its read loop. handler may be nil if this
// peer only makes outbound calls.
func NewConn(rw io.ReadWriteCloser, handler Handler) *Conn {
	c := &Conn{
		rw:            rw,
		reader:        bufio.NewReader(rw),
		handler:       handler,
		pending:       make(map[string]chan *Message),
		notifications: make(chan *Message, 256),
		done:          make(chan struct{}),
	}
	go c.readLoop()
	go c.notificationLoop()
	return c
}

// Request sends a request and blocks until the response arrives or ctx is done.
func (c *Conn) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}

	id := strconv.FormatInt(atomic.AddInt64(&c.nextID, 1), 10)
	ch := make(chan *Message, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, &RPCError{Code: codeInternalError, Message: "connection is closed"}
	}
	c.pending[id] = ch
	c.mu.Unlock()

	idRaw := json.RawMessage(id)
	if err := c.write(&Message{JSONRPC: "2.0", ID: &idRaw, Method: method, Params: raw}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify sends a fire-and-forget notification (no response expected).
func (c *Conn) Notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(&Message{JSONRPC: "2.0", Method: method, Params: raw})
}

// Close shuts the connection and fails every in-flight request.
func (c *Conn) Close() error {
	c.shutdown(&RPCError{Code: codeInternalError, Message: "connection closed by caller"})
	return c.rw.Close()
}

func (c *Conn) write(msg *Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeMessage(c.rw, msg)
}

func (c *Conn) readLoop() {
	for {
		msg, err := readMessage(c.reader)
		if err != nil {
			c.shutdown(&RPCError{Code: codeInternalError, Message: err.Error()})
			return
		}
		c.dispatch(msg)
	}
}

func (c *Conn) dispatch(msg *Message) {
	if msg.isResponse() {
		c.resolveResponse(msg)
		return
	}
	if msg.ID != nil {
		go c.handleRequest(msg)
	} else {
		// Queue for the ordered worker; drop if the connection is closing.
		select {
		case c.notifications <- msg:
		case <-c.done:
		}
	}
}

func (c *Conn) notificationLoop() {
	for {
		select {
		case msg := <-c.notifications:
			if c.handler != nil {
				_, _ = c.handler(msg.Method, msg.Params)
			}
		case <-c.done:
			return
		}
	}
}

// resolveResponse matches by the raw id bytes, so numeric and string ids both
// work (LSP servers may echo either form).
func (c *Conn) resolveResponse(msg *Message) {
	key := strings.Trim(string(*msg.ID), `"`)
	c.mu.Lock()
	ch := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

func (c *Conn) handleRequest(msg *Message) {
	resp := &Message{JSONRPC: "2.0", ID: msg.ID}
	var result any
	var err error
	if c.handler == nil {
		err = &RPCError{Code: codeMethodNotFound, Message: "no handler for " + msg.Method}
	} else {
		result, err = c.handler(msg.Method, msg.Params)
	}
	if err != nil {
		if rpcErr, ok := err.(*RPCError); ok {
			resp.Error = rpcErr
		} else {
			resp.Error = &RPCError{Code: codeInternalError, Message: err.Error()}
		}
	} else {
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			resp.Error = &RPCError{Code: codeInternalError, Message: marshalErr.Error()}
		} else {
			resp.Result = raw
		}
	}
	_ = c.write(resp)
}

func (c *Conn) shutdown(reason *RPCError) {
	c.closeFn.Do(func() {
		close(c.done)
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = make(map[string]chan *Message)
		c.mu.Unlock()
		for _, ch := range pending {
			ch <- &Message{JSONRPC: "2.0", Error: reason}
		}
	})
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}

// writeMessage encodes a message as an LSP frame:
// `Content-Length: N\r\n\r\n<utf8 json>`.
func writeMessage(w io.Writer, msg *Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

const contentLengthPrefix = "content-length:"

// readMessage reads one frame: the header block terminated by a blank line,
// then exactly Content-Length bytes of body.
func readMessage(r *bufio.Reader) (*Message, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), contentLengthPrefix) {
			value := strings.TrimSpace(line[len(contentLengthPrefix):])
			contentLength, err = strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("lsp: frame is missing a Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

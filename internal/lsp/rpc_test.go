package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// pipePair builds two connected ReadWriteClosers (client side, server side).
type pipeEnd struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeEnd) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeEnd) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeEnd) Close() error {
	_ = p.w.Close()
	return p.r.Close()
}

func pipePair() (pipeEnd, pipeEnd) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return pipeEnd{r: cr, w: cw}, pipeEnd{r: sr, w: sw}
}

func TestRequestResponseRoundTrip(t *testing.T) {
	clientEnd, serverEnd := pipePair()
	server := NewConn(serverEnd, func(method string, params json.RawMessage) (any, error) {
		if method != "echo" {
			t.Errorf("unexpected method %q", method)
		}
		var payload map[string]string
		_ = json.Unmarshal(params, &payload)
		return payload, nil
	})
	defer server.Close()
	client := NewConn(clientEnd, nil)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := client.Request(ctx, "echo", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	must := json.Unmarshal(raw, &got)
	if must != nil || got["k"] != "v" {
		t.Fatalf("round trip: %s", raw)
	}
}

func TestStringIDResponse(t *testing.T) {
	// A server that echoes the id back as a string must still resolve.
	clientEnd, serverEnd := pipePair()
	client := NewConn(clientEnd, nil)
	defer client.Close()

	go func() {
		// Hand-rolled server: read one frame, respond with a *string* id.
		reader := bufio.NewReader(serverEnd)
		msg, err := readMessage(reader)
		if err != nil {
			return
		}
		id := json.RawMessage(`"` + string(*msg.ID) + `"`)
		_ = writeMessage(serverEnd, &Message{JSONRPC: "2.0", ID: &id, Result: json.RawMessage(`"ok"`)})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := client.Request(ctx, "anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"ok"` {
		t.Fatalf("got %s", raw)
	}
}

func TestNotificationOrdering(t *testing.T) {
	clientEnd, serverEnd := pipePair()
	got := make(chan string, 10)
	client := NewConn(clientEnd, func(method string, _ json.RawMessage) (any, error) {
		got <- method
		return nil, nil
	})
	defer client.Close()
	server := NewConn(serverEnd, nil)
	defer server.Close()

	for _, name := range []string{"a", "b", "c"} {
		if err := server.Notify(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		select {
		case name := <-got:
			if name != want {
				t.Fatalf("order broken: got %q want %q", name, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for notification")
		}
	}
}

func TestShutdownFailsInflight(t *testing.T) {
	clientEnd, _ := pipePair() // server never answers
	client := NewConn(clientEnd, nil)

	done := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), "hang", nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = client.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("in-flight request should fail on close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request hung after close")
	}
}

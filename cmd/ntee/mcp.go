package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickooan/ntee-editor/internal/app"
	"github.com/nickooan/ntee-editor/internal/mcpserver"
)

// serveAgentTools is --mcp: the agent edit tools over MCP on stdio until
// the client closes stdin. Each call finds the running editor like --msg
// does, so the server outlives editor restarts. It returns the exit code.
func serveAgentTools(editorRoot string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := mcpserver.Serve(ctx, os.Stdin, os.Stdout, mcpserver.Config{
		FindSocket: func(paths []string) (string, error) {
			return findEditorSocket(editorRoot, paths)
		},
		Version:        app.Version,
		RequestTimeout: editRequestTimeout,
		EndTimeout:     editEndTimeout,
		SandboxTimeout: editSandboxTimeout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ntee --mcp:", err)
		return 1
	}
	return 0
}

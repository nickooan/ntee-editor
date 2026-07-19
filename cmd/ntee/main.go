// ntee is a Sublime-style TUI text editor: file tree on the left, highlighted
// content on the right, a : command bar at the bottom, and state persisted in
// ntee-db (recent files, undo snapshots, session).
//
// Usage: ntee [project-root]   (defaults to the current directory)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/app"
	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/lspsetup"
	"github.com/nickooan/ntee-editor/internal/store"
)

const version = "0.1.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	prepareLSP := flag.Bool("prepare-lsp", false, "install language servers and write config, then exit")
	assumeYes := flag.Bool("yes", false, "skip the confirmation prompt for --prepare-lsp")
	flag.Parse()
	if *showVersion {
		fmt.Println("ntee-editor " + version)
		return
	}
	if *prepareLSP {
		confirm := func() bool {
			if *assumeYes {
				return true
			}
			var answer string
			_, _ = fmt.Scanln(&answer)
			return answer == "y" || answer == "Y" || answer == "yes"
		}
		if err := lspsetup.Prepare(os.Stdout, confirm); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	root := flag.Arg(0)
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid root:", err)
		os.Exit(1)
	}
	if info, err := os.Stat(absRoot); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "not a directory:", absRoot)
		os.Exit(1)
	}

	cfg := config.Load(absRoot)

	// Per-project ntee-db store; fall back to in-memory (undo only, nothing
	// persists) when the store's single-writer lock is held by another
	// instance of this project.
	var db store.Backend
	notice := ""
	if s, err := store.Open(absRoot, cfg.Editor.MaxSnapshots); err != nil {
		db = store.NewMemory()
		notice = "persistence disabled (store unavailable)"
	} else {
		db = s
	}
	defer db.Close()

	// Language servers (gopls, typescript-language-server) start lazily per
	// language; diagnostics flow into the program via the sink.
	var reg lsp.Registry = lsp.NewNoopRegistry()
	var manager *lsp.Manager
	if cfg.LSP.Enabled {
		manager = lsp.NewManager(cfg, absRoot)
		reg = manager
	}

	program := tea.NewProgram(app.New(cfg, db, absRoot, notice, reg), tea.WithAltScreen())
	if manager != nil {
		manager.SetSink(func(msg any) { program.Send(msg) })
		defer manager.ShutdownAll()
	}
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

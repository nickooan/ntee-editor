// ntee is a Sublime-style TUI text editor: file tree on the left, highlighted
// content on the right, a : command bar at the bottom, and state persisted in
// ntee-db (recent files, undo snapshots, session).
//
// Usage: ntee [dir | file[#L<line>]]   (defaults to the current directory;
// a file opens in edit mode with its folder as the project, and a file that
// doesn't exist yet opens empty and is created on the first save)
//
//	ntee --msg [--msg-note <text|->] <open|review|help> [paths…]   send a request to a running editor
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"gopkg.in/yaml.v3"

	"github.com/nickooan/ntee-editor/internal/app"
	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/lspsetup"
	"github.com/nickooan/ntee-editor/internal/store"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	prepareLSP := flag.Bool("prepare-lsp", false, "install language servers (optionally only the named languages) and write config, then exit")
	assumeYes := flag.Bool("yes", false, "skip the confirmation prompt for --prepare-lsp")
	disableLSP := flag.Bool("disable-lsp", false, "disable LSP for the named languages (or 'all'); writes the user config")
	enableLSP := flag.Bool("enable-lsp", false, "re-enable LSP for the named languages (or 'all'); writes the user config")
	sendMsg := flag.Bool("msg", false, "send a request to the running editor of this directory: --msg open <file>#L<n> · --msg --msg-note <text> open <file>#L<n> · --msg review <files…> (or paths on stdin) · --msg help")
	msgRoot := flag.String("msg-root", "", "with --msg: the editor's opened folder (default: the nearest running one above the current directory)")
	msgBase := flag.String("msg-base", "", "with --msg open/review: the revision to compare the current files against (default HEAD)")
	msgNote := flag.String("msg-note", "", "with --msg open: a note shown in a box next to the line, light markdown (needs <file>#L<n>); '-' reads it from stdin")
	flag.Parse()
	if *showVersion {
		fmt.Println("ntee-editor " + app.Version)
		return
	}
	if *sendMsg {
		os.Exit(sendMessage(flag.Args(), *msgRoot, *msgBase, *msgNote))
	}
	if *disableLSP || *enableLSP {
		if *disableLSP && *enableLSP {
			fmt.Fprintln(os.Stderr, "use either --disable-lsp or --enable-lsp, not both")
			os.Exit(1)
		}
		if err := toggleLSP(flag.Args(), *enableLSP); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
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
		if err := lspsetup.Prepare(os.Stdout, confirm, flag.Args()...); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	argument := flag.Arg(0)
	if argument == "" {
		argument = "."
	}
	absRoot, startFile, startLine, err := resolveStartTarget(argument)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// The editor proper runs in a helper so its defers (store close, language
	// server shutdown) execute even on the error path — os.Exit skips defers,
	// so it must be the very last thing main does.
	os.Exit(runEditor(absRoot, startFile, startLine))
}

// resolveStartTarget reads the command-line argument: a directory opens as
// the project; anything else is a file (dotfiles included, optionally with a
// "#L<n>" anchor) opened in edit mode with its own folder as the project. A
// file that doesn't exist yet is fine as long as its folder does — it opens
// empty and is created on the first save.
func resolveStartTarget(argument string) (root, fileName string, line int, err error) {
	absolute, err := filepath.Abs(argument)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid path: %w", err)
	}
	if info, statErr := os.Stat(absolute); statErr == nil {
		if info.IsDir() {
			return absolute, "", 0, nil
		}
		// An existing file is taken as named, even one with "#" in its name.
		return filepath.Dir(absolute), filepath.Base(absolute), 0, nil
	}
	pathText, line := app.SplitLineAnchor(argument)
	if absolute, err = filepath.Abs(pathText); err != nil {
		return "", "", 0, fmt.Errorf("invalid path: %w", err)
	}
	if info, statErr := os.Stat(absolute); statErr == nil && info.IsDir() {
		return "", "", 0, fmt.Errorf("a line anchor needs a file, not a directory: %s", argument)
	}
	folder := filepath.Dir(absolute)
	if info, statErr := os.Stat(folder); statErr != nil || !info.IsDir() {
		return "", "", 0, fmt.Errorf("no such file or directory: %s", argument)
	}
	return folder, filepath.Base(absolute), line, nil
}

// runEditor owns every resource with a shutdown obligation and returns the
// process exit code.
func runEditor(absRoot, startFile string, startLine int) int {
	cfg, warnings := config.LoadWithWarnings(absRoot)

	// A single file keeps its own store (keyed by the file's path), so the
	// folder's tabs, session, and index never show up around it. Fall back to
	// in-memory (undo only, nothing persists) when another instance holds the
	// store's single-writer lock.
	storeKey := absRoot
	if startFile != "" {
		storeKey = filepath.Join(absRoot, startFile)
	}
	var db store.Backend
	if s, err := store.Open(storeKey, cfg.Editor.MaxSnapshots); err != nil {
		db = store.NewMemory()
		warnings = append(warnings, "persistence disabled: "+firstLine(err.Error()))
	} else {
		db = s
	}
	defer db.Close()
	notice := strings.Join(warnings, " · ")

	var reg lsp.Registry = lsp.NewNoopRegistry()
	var manager *lsp.Manager
	if cfg.LSP.Enabled {
		manager = lsp.NewManager(cfg, absRoot)
		reg = manager
	}

	model := app.New(cfg, db, absRoot, notice, reg)
	var settingsStore store.ExternalSettingsStore = store.NewMemoryExternalSettings(store.DefaultExternalSettings)
	if globalDir, err := store.GlobalDir(); err == nil {
		model = model.WithOpCommandStore(store.NewGlobalOpCommands(globalDir))
		settingsStore = store.NewGlobalExternalSettings(globalDir)
	}

	var historyStore store.ExternalHistoryStore = store.NewMemoryExternalHistory()
	var receiver *app.ExternalReceiver
	if startFile == "" {
		if history, err := store.NewExternalHistory(absRoot); err == nil {
			historyStore = history
		}
		receiver = app.NewExternalReceiver(absRoot, settingsStore, historyStore)
	}
	server, socketPath, socketProblem := listenForExternalMessages(absRoot, receiver)
	if server != nil {
		defer server.Close()
	}
	model = model.WithExternal(settingsStore, historyStore, socketPath, socketProblem)
	if startFile != "" {
		model = model.WithOnlyFile(startFile).WithStartFile(startFile, startLine)
	}

	program := tea.NewProgram(model)
	if receiver != nil {
		receiver.SetSink(func(msg any) { program.Send(msg) })
	}
	if manager != nil {
		manager.SetSink(func(msg any) { program.Send(msg) })
		defer manager.ShutdownAll()
	}
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// listenForExternalMessages starts the project's external-message socket. It
// listens whether or not messages are enabled, so a sender always gets an
// answer ("disabled") instead of a dead socket. A nil receiver is
// single-file mode (`ntee <file>`), which never listens: requests name other
// files of the folder, which that editor deliberately doesn't show. problem
// says why there is no server.
func listenForExternalMessages(absRoot string, receiver *app.ExternalReceiver) (server *extmsg.Server, socketPath, problem string) {
	socketPath, err := store.ExternalSocketPath(absRoot)
	switch {
	case receiver == nil:
		return nil, socketPath, "single-file mode — open a folder to take external messages"
	case err != nil:
		return nil, socketPath, firstLine(err.Error())
	}
	server, err = extmsg.Listen(socketPath, receiver.Handle)
	if err != nil {
		return nil, socketPath, firstLine(err.Error())
	}
	return server, socketPath, ""
}

// firstLine clips an error message to its first line for the status bar.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// knownLanguages is the union of the built-in defaults, the --prepare-lsp
// recipes, and any languages already in the user config — the names
// --disable-lsp/--enable-lsp accept (plus "all").
func knownLanguages() []string {
	names := map[string]bool{}
	for name := range config.Default().Languages {
		names[name] = true
	}
	for name := range lspsetup.Recipes() {
		names[name] = true
	}
	if path, err := config.ConfigPath(); err == nil {
		var user config.Config
		if data, err := os.ReadFile(path); err == nil {
			if yaml.Unmarshal(data, &user) == nil {
				for name := range user.Languages {
					names[name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// toggleLSP validates the language names and writes their enable flag (or the
// global lsp.enabled for "all") into the user config.
func toggleLSP(langs []string, enabled bool) error {
	known := knownLanguages()
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	if len(langs) == 0 {
		return fmt.Errorf("usage: ntee --%s-lsp <language>... (or 'all')\nknown languages: %s", verb, strings.Join(known, ", "))
	}
	for i, l := range langs {
		langs[i] = strings.ToLower(l)
	}
	for _, l := range langs {
		if l == "all" || slices.Contains(known, l) {
			continue
		}
		return fmt.Errorf("unknown language %q\nknown languages: all, %s", l, strings.Join(known, ", "))
	}
	path, err := config.SetLanguagesEnabled(langs, enabled)
	if err != nil {
		return err
	}
	fmt.Printf("LSP %sd for %s — written to %s\n", verb, strings.Join(langs, ", "), path)
	return nil
}

# ntee-editor

A Sublime-style code editor that lives in your terminal. Built on Bubble Tea,
powered by [ntee-db](https://github.com/nickooan/ntee-db) — your tabs, drafts,
undo history, and session all survive relaunch.

```
┌ ntee-editor · ~/projects/my-app ──────────────────────────────┐
│ file tree          │  tab1.go  tab2.ts  (red = unsaved)       │
│ (cursor-driven,    │ ─────────────────────────────────────────│
│  git-aware colors) │  1 │ package main        ← syntax colors │
│                    │  2 │                     ← ● diagnostics │
│                    │  3 │ func main() {       ← click to move │
└───────────────────────────────────────────────────────────────┘
 @query > _                                  status / command bar
```

## Highlights

- **Instant project navigation** — fuzzy file finder, repo-wide content grep,
  and a live query bar that expands the file tree as you type.
- **Real code intelligence** — LSP-backed autocomplete, diagnostics in the
  gutter, go-to-definition / find-references, for Go, TypeScript/React, Python,
  Ruby, Java, Kotlin, Vue. One command installs the servers.
- **Find & replace with live preview** — see every replacement rendered in the
  buffer *before* you commit it.
- **Mouse support** — click anywhere in the file to place the cursor.
- **Everything persists** — tabs, unsaved drafts, undo timeline, and session
  are stored per-project in ntee-db and restored on relaunch.
- **Built-in inspection dashboard** — see store disk usage and language-server
  status, compact the database, and start/stop LSPs without leaving the editor.
- **Op-commands** — save shell command templates once (`terraform plan
  -var-file={$1}`, `go test {$fpath}`) and run them on the open file with
  `Ctrl+R`, output streaming live.
- **Driven by your coding agent** — `ntee --msg open file.go#L42` jumps the
  running editor to a line, and `ntee --msg review <files>` opens a
  multi-file, multi-repo diff review of what the agent just changed
  ([below](#external-messages--review)).

## Install

macOS — one command via our Homebrew tap. Each release ships a universal
binary (Intel + Apple Silicon), so there's nothing to pick:

```sh
brew install --cask nickooan/tap/ntee
```

**Updating**: `brew upgrade --cask ntee`.

On Linux, or to build from source, install with Go 1.25.13+ — the binary lands
in `~/go/bin`, make sure it's on your PATH:

```sh
go install github.com/nickooan/ntee-editor/cmd/ntee@latest
```

Then set up language servers for the languages you use — one command, see
[Installing language servers](#installing-language-servers):

```sh
ntee --prepare-lsp
```

## Quick start

```sh
ntee                  # open the current directory
ntee <path>           # open a project
ntee .env             # open just this file (dotfiles too) — the sidebar and search show only it
ntee src/app.go#L42   # …with the cursor on line 42
ntee notes.txt        # a file that doesn't exist yet opens empty; Ctrl+S creates it
```

Five things to know in your first five minutes:

1. **Type to navigate.** The `@query >` bar at the bottom drives the sidebar:
   type a path fragment and directories expand, the best match highlights, and
   a popup suggests completions. `Enter` opens a file **straight into edit
   mode**.
2. **`Ctrl+P` finds files, `Ctrl+G` greps contents** — anywhere, any time.
3. **Click to place the cursor**, type to edit, `Ctrl+S` to save.
4. **`Esc` walks you back out** — clears the selection, then leaves edit mode,
   then goes up a directory.
5. **`Ctrl+Q` quits.** Your tabs, drafts, and undo history will be there when
   you come back.

---

## Keymap

The keys below are the defaults. Every `Ctrl`/`Shift+Tab` action under
**Everywhere** and the edit-mode chords (save, undo, redo, find, jump/back,
select, exec) can be rebound — see [Custom keybindings](#custom-keybindings).

### Everywhere

| Key | Action |
|---|---|
| `Ctrl+P` | **Goto file** — fuzzy finder; empty query lists recent files. Append `#L<line>` (e.g. `build.sh#L8`) to open on that line |
| `Ctrl+U` | Goto **uncommitted** file — same finder, limited to git-dirty paths |
| `Ctrl+W` | **Workspace repo** — re-root the editor at a nested git repo under the opened directory (pick the top `name/` row to go back to the whole workspace). The file tree, query bar, `Ctrl+P`/`Ctrl+U`/`Ctrl+G`, change highlights, and git views then cover that repo only, as if you had opened it directly; the title bar shows `working repo: …` in orange next to the path. Each repo remembers its own tabs, open file, and tree position (also across relaunch), and switching stashes unsaved edits as a draft. Refused when the opened directory is already a git repo |
| `Ctrl+G` | **Grep the repo** — colored preview on top, results below; `↑/↓` + `Enter` jumps |
| `Ctrl+T` | **Inspection dashboard** — store stats + LSP control + op-commands + keybinds + code style + external messages ([below](#inspection-mode-ctrlt)) |
| `Ctrl+R` | **Run an op-command** on the open file — pick a saved command, fill its `{$1}` `{$2}` args, watch the output stream ([below](#op-commands-ctrlr)) |
| `Shift+Tab` | Cycle the focused tab (wraps) |
| `Ctrl+Q` / `Ctrl+C` | Quit (session saved). `Ctrl+C` always quits, whatever the bindings |

### Query bar (home)

| Key | Action |
|---|---|
| type a path / fragment | expand directories · highlight best match · suggest completions |
| `↑/↓` (popup open) | move the popup selection — previews in the bar, highlights in the sidebar |
| `↑/↓` (no popup) | scroll the open file |
| `Shift+↑/↓` | walk the sidebar tree row-by-row (never expands) |
| `Enter` | directory → enter it · file → open in edit mode · `:cmd` → run command |
| `path#L<line>` | optional line anchor, GitHub style (`scripts/build.sh#L8`, `#l8`, `#L8-L12`) — matching ignores it; `Enter` opens the file on that line (past the end → last line) |
| `Tab` | edit the currently open file |
| `Ctrl+F` | find in the open file |
| `Esc` | go up one directory |

### Edit mode

| Key | Action |
|---|---|
| **mouse click** | move the cursor to the clicked position (gutter click → column 0). Hold `Shift` for native terminal text selection |
| **`Ctrl`+click** | jump to definition at the clicked token (same as `Ctrl+J`). Some terminals capture Ctrl+click as a right-click — then use `Ctrl+J` |
| **wheel / two-finger scroll** | scroll up/down (moves the cursor); horizontal scroll does nothing |
| `Ctrl+S` | save (also clears the file's stashed draft) |
| `Ctrl+Z` / `Ctrl+Y` | undo / redo — snapshot bursts, persisted across relaunch |
| `Ctrl+A` | progressive select: word → line; then `Shift+↑/↓` extends line-wise |
| `Ctrl+F` | find in file ([below](#find--replace-ctrlf)) |
| `Ctrl+J` | **jump to definition** (or the file path under the cursor); on a definition line it finds **references** instead. Multiple hits open a picker with a live code preview |
| `Ctrl+O` | jump back (restores file, cursor, scroll; 20-deep trail) |
| `Ctrl+E` | open the `@exec >` command bar ([below](#exec-bar-ctrle)) |
| `PgUp` / `PgDn` | page with a one-line overlap |
| `Home` / `End` | line start / end |
| `Esc` | clear selection → then discard unsaved edits and return to the query bar |

**Autocomplete** pops up on its own as you type an identifier or `.` —
`↑/↓` selects, `Tab`/`Enter` accepts, keep typing to filter, `Esc` dismisses.
Powered by the file's language server.

### Find & replace (`Ctrl+F`)

Type to search — matches highlight live (case-insensitive; regex supported,
falling back to literal). `↑/↓` cycles the focused match (orange), `Enter`
jumps the cursor to it, `Esc` backs out.

Press **`Ctrl+E` while matches are highlighted** to enter the replace bar:

| Command | Action |
|---|---|
| `c <text>` | replace the **focused** match — then focus lands on the next one, so you can `c` again |
| `mlc <text>` | **m**ulti-**l**ine **c**ursor: replace **all** matches at once |

As you type the replacement, the buffer shows a **live preview** — target spans
render in green exactly as they'll look after `Enter`. Only the matched span is
touched (`search54321` + `c search123` → `search12354321`); a bare `c`/`mlc`
deletes the match. Every replace — even `mlc` across a hundred lines — is one
`Ctrl+Z` step.

### Exec bar (`Ctrl+E`)

Editor commands with Tab-completed suggestions:

| Command | Action |
|---|---|
| `copy` (`cp`) `[a-b\|all\|fpath]` | copy the selection, a line range, the whole buffer, or the file's path (relative to its git repo when it is in one) |
| `cpfp` / `cpafp` | copy the file's repo-relative / absolute path |
| `cpfpl` | copy the repo-relative path with the cursor line, e.g. `scripts/build.sh#L8` — paste it into the query bar or `Ctrl+P` to come back here |
| `jump` (`jp`) `<line\|top\|end>` | go to a line (lands ~30% from the top) |
| `git scf` | **s**olve **c**on**f**lict: review the buffer's conflicts interactively — ours tinted green, theirs blue, markers yellow. `↑/↓` move line by line, `Shift+↑/↓` jump between conflict blocks; on a marker line a picker pops up: `←/→` choose **Use HEAD / Use \<branch\> / Use both**, `Enter` applies (each apply is one `Ctrl+Z` step), `Esc` back to editing at the cursor — applied resolutions stay, untouched conflicts remain |
| `git diff [rev]` | review the file's uncommitted changes (or its diff against `rev`) in a read-only GitHub-style view: added lines green with a `+` gutter marker, removed lines red with `-` (no number). `↑/↓`/`PgUp`/`PgDn` review, `Shift+↑/↓` jump between change hunks, `Ctrl+J` jump to definition (`Ctrl+O` returns to the review), `Esc` back to editing at the reviewed line. In a workspace the file's own repo is used |
| `git blame` | annotate every line with its author and commit date in a read-only view — the gutter shows `author date │` instead of line numbers; unsaved or uncommitted lines show a dimmed `uncommitted` placeholder. `↑/↓`/`PgUp`/`PgDn` move, `Shift+↑/↓` jump between commit groups, `Ctrl+J` jump to definition (`Ctrl+O` returns to the blame), `Esc` back to editing at the cursor line. In a workspace the file's own repo is used |
| `tab <name\|cl\|cr>` | switch tab / close-left / close-right |

### Command bar (`:`)

`jump <line|top|end>` (alias `jp`) · `tab <name|cl|cr>` · `revert` (restore
last saved snapshot) · `refresh` (re-scan the file tree).

### Inspection mode (`Ctrl+T`)

A dashboard for the editor's own machinery. `Shift+↑/↓` switches the left menu;
`Esc` returns to where you were.

- **ntee-db** — the project store's disk usage: live records, main-log size
  with dead-space percentage, blob usage, orphaned bytes.
- **lsp** — every configured language server with its live status:
  **running** (green) · **stopped** (yellow — starts on demand) ·
  **disabled** (gray, with the reason). Press `→` to move into the list:
  `↑/↓` picks a language and `Enter` opens the actions that fit its state.
  **start / stop / restart** act on this session only and leave your config
  alone. A stopped server starts again the next time a file needs it.
  **enable / disable** also save `enable: true` / `false` to your config, so
  they last across restarts. `↑/↓` chooses, `Enter` applies, `Esc` closes the
  dropdown, then goes back.
- **system** — version and syntax color style. Press `→` to move into the
  style list: `↑/↓` browses with a live code preview in that style, `Enter`
  applies it (saved to your config), `←`/`Esc` goes back.
- **op-commands** — your saved shell command templates as a KEY / TYPE /
  COMMAND table. Press `→` to move into the table; there `↑/↓` picks a row,
  `Enter` edits it (or adds a new one on `+ New command`), `Tab`/`Shift+Tab`
  moves between key, type and command, `Ctrl+S` saves, `Esc` reverts the
  edit, and `d` deletes after a `y/n` confirmation. `←`/`Esc` goes back to the
  menu. TYPE is the file types the command is for, e.g. `go`, `tf, tfvars`, or
  `*` for every file (left empty it saves as `*`). Typing a type suggests common
  ones (`go`, `rb`, `sh`, `tfvars`, …, the open file's type first). One key can
  exist once per type, so `test` can be `go test ./...` for `go` and
  `bundle exec rspec {$fpath}` for `rb`. A key whose types clash with an
  existing command turns red as you type, and saving it pops up an alert. Typing `{` or `$` in a command
  opens placeholder suggestions: the next free argument (`{$2}` when the command
  already has `{$1}`), the existing ones, and `{$fpath}`/`{$dpath}`. `↑/↓`
  chooses, `Tab`/`Enter` inserts, `Esc` closes the menu. Placeholders are
  colour-coded in the table: arguments purple, built-ins blue, `{#"…"}` fragments
  green (with their `$n` purple), broken ones red.
- **keybinds** — every rebindable action with its key. Press `→` to move into
  the list: `↑/↓` picks an action, `Enter` then **press the new chord** to bind
  it (`Esc` cancels), `r` resets that action to its default, `R` (or `Enter` on
  the last row) resets **all** of them after a `y/n` confirmation. A key that's
  already taken, or isn't a chord, is refused in the status bar. Custom keys
  are highlighted next to their default. Changes apply immediately and are
  saved to your config — see [Custom keybindings](#custom-keybindings).
- **code style** — the tab size, as a LANGUAGE | TAB SIZE | DESCRIPTION table
  laid out like keybinds. **all files** is the size for everything; each
  language (go, typescript, python, yaml, markdown, makefile, …) uses it until
  you give that language its own. The size is how many spaces `Tab` inserts
  *and* how wide tab characters in a file are drawn — in the editor, diffs,
  review, previews and notes. Press `→` to move in: `↑/↓` picks a row, `Enter`
  types a size (1–16, `Enter` saves, `Esc` cancels), `+`/`-` step it, `r` lets
  a language use **all files** again (or puts **all files** back to 4), `R`
  resets everything after a `y/n`. In the bar: `tab 2`, `tab go 8`,
  `tab go default`, `tab reset`. Saved to your config as `tab_width` and
  `language_tab_widths`. A size set by the project's `.ntee-editor.yaml` is
  tagged **project** and can only be changed in that file.
- **external** — whether other programs may send this editor requests
  ([External messages & review](#external-messages--review)). Shows the
  socket the editor listens on, the **accept messages** switch (off by
  default), the **history limit** (how many recent messages each project
  keeps, 1–100, default 10), and how many this project has saved. Press `→`
  to move in: `↑/↓` picks a row, `Enter` toggles the switch, edits the limit
  (type a number, `Enter` saves, `Esc` cancels), or opens the **history** —
  every saved message on the left, its details (including the **git hash**
  it compares against, `HEAD` by default) and raw text on the right,
  `Enter` to replay one, `Esc` back. `→` moves focus to the details so `↑/↓`
  (or the wheel) scroll a long path list; `←` or `Esc` returns to the list.

The `@inspection >` bar takes:

| Command | Action |
|---|---|
| `db compact` | drop dead records from the main log (runs in the background) |
| `db relieve` | also rewrite the blob store, releasing orphaned blobs |
| `lsp start\|stop\|restart <lang\|all>` | start, stop, or restart the server for **this session** only; the config is unchanged (a stopped server starts again when a file needs it) |
| `lsp enable <lang\|all>` | start the server **now** and persist `enable: true` to your config |
| `lsp disable <lang\|all>` | stop the server and persist `enable: false` |
| `keybind <action> <key>` | bind an action, e.g. `keybind goto_file ctrl+n` (saved to your config) |
| `keybind <action> default` | restore one action's default key |
| `keybind reset` | restore every default key |
| `ext on` / `ext off` | accept or refuse external messages (all projects) |
| `ext limit <n>` | keep the latest `n` messages per project (1–100) |
| `ext history` | open the external-message history |

LSP commands act live *and* write `~/.config/ntee-editor/config.yaml` (previous
file backed up to `config.yaml.bak`), so the change sticks across restarts. If a
server fails to start (e.g. binary missing), the error shows right in the bar.

### Op-commands (`Ctrl+R`)

An op-command is a named shell command template saved in the inspection
dashboard's **op-commands** table. It can use these placeholders:

| Placeholder | Replaced with |
|---|---|
| `{$1}`, `{$2}`, … | the 1st, 2nd, … argument you type when running it |
| `{#"-P $2"}` | `-P <argument 2>` when you give argument 2, and nothing at all when you leave it out, so no dangling `-P`. Joined forms work too: `{#"--parallel=$2"}`. The text around `$2` can only be plain words and spaces |
| `{$1 or master}` | argument 1, or `master` when you leave it out. Quote a default with spaces: `{$2 or "other word"}`. `{$3 or ""}` makes the placeholder disappear when omitted (no empty `''` argument) |
| `{$fpath}` | the open file's path, relative to the workspace directory (e.g. `apps/web/main.go`) |
| `{$dpath}` | the open file's directory, relative to the workspace directory (e.g. `apps/web`; `.` at the top level) |

Defaults never force you to type around them. In
`deploy {$1} {$2 or master} {$3} {$4 or ""}`, `$1` and `$3` are the required
arguments:
- `deploy a b` fills just those (`$1=a $3=b`) and uses the defaults for the rest.
- `deploy a b c` fills `$1 $2 $3` in order and leaves `$4` to its default.

The args box shows which value lands where before you run.

To pass flags the template doesn't name, put them after `--`, like `npm run x -- …`:
`test 10 -- -P 30` fills `{$1}` with `10` and adds `-P 30` to the end of the
command. Words after `--` are quoted like normal arguments and don't count
toward the argument limit. Write `'--'` to pass a literal `--` as an argument.

`Ctrl+R` lists only the commands whose TYPE matches the open file (plus the
`*` ones). When a key exists both for the file's type and for `*`, the one for
the type wins, so `test` on a `.go` file runs the go version.

With a file open, press `Ctrl+R`, type to fuzzy-find a command, and press
`Enter`. If the template takes arguments, type them (`"a b"` groups words into
one argument). You can also type them on the same line after the name:
`test 10` + `Enter` runs `test` with `10` as `{$1}`, and the box shows the
resulting command before you press `Enter`. A live preview shows the exact command line before `Enter` runs
it. Commands without arguments run straight away. Values are shell-quoted only
when they need it, so `master` is passed as `master` and `my file.tf` as
`'my file.tf'`. The program gets the same arguments either way. Because of
that, write placeholders bare: `git commit -m {$1}`, not `-m "{$1}"`. A
placeholder inside quotes (or right after `\` or `$`) is rejected when you
save, since the quoting would no longer protect it. Both inputs
edit like a text field: `←`/`→` move the cursor and typing inserts at it, so
you can fix a typo in the name or an argument without retyping the line.

Commands run on the file that is open in the editor (the active tab), which the
overlay names as `file …`, not on whatever the file tree highlights. They read
the file as saved on disk, so if it has unsaved changes the overlay warns you:
save with `Ctrl+S` first if the command should see them.

The command runs with `sh -c` in the workspace directory (the one the editor
was opened on, even after `Ctrl+W` switched to a nested repo), so `{$fpath}` and
`{$dpath}` resolve from there, and its output (stdout and
stderr) streams into the overlay as it arrives. When the process exits, the
footer turns green (`✓ finished`), or red with the exit status if it failed.
Press `Esc` to close. `Esc` while the command is still running kills it.
`http(s)://` links in the output are underlined; click one, even while the
command is still running, to open it in your default browser.

Op-commands are stored in `~/.ntee-editor/global/` and shared by every project.

---

## External messages & review

A running editor can take requests from other programs — your coding agent,
a script, a git hook. Each editor listens on a private unix socket for the
folder it opened (only your user can connect). Turn it on once in
`Ctrl+T` → **external** (or `ext on` in that bar); until then senders are told
messages are disabled.

```sh
ntee --msg open internal/app/app.go#L120        # jump to a line
ntee --msg review internal/app/app.go web/src/main.ts old/removed.go
git diff --name-only | ntee --msg review         # paths on stdin
ntee --msg --msg-base HEAD~3 review $(git diff --name-only HEAD~3)
ntee --msg --msg-base a1b2c3d open internal/app/app.go#L120   # diff against a commit
ntee --msg --msg-note "why this line matters" open internal/app/app.go#L120
ntee --msg --msg-note - open internal/app/app.go#L120 <<'EOF'   # a longer note on stdin
## Cache guard
- skips the re-wrap unless the width changed
EOF
ntee --msg help                                  # the full guide
```

`ntee --msg` finds the editor by walking up from the current directory:
the nearest folder that a running editor has open gets the message
(`--msg-root <dir>` names one instead). Run outside every open folder, it
looks upward from the first path's folder. The same folder open in two editors
has one listener, the first one. Single-file mode (`ntee <file>`) never
listens. See [doc/extmsg.md › Which editor gets a message](doc/extmsg.md#which-editor-gets-a-message)
for the socket naming and the edge cases.

Paths are relative to where you run it, or absolute, and must be inside the
editor's folder. It prints one reply line — `ok …` or `error …` (exit code 1).

Either request may name a **base revision** with `--msg-base <rev>` (a commit
hash, tag, branch, `HEAD~2`; on the wire it follows the command on the first
line, `open <rev>` / `review <rev>`). It is always compared with the current
files on disk, uncommitted changes included; without one the base is `HEAD`.
The history saves it and shows it on the message's **git hash** row, and a
replay compares against the same revision.

- **open** jumps straight there from the query view. If you are busy —
  editing, in a bar, a diff, a preview — a dialog asks first (**Jump** /
  **Ignore**), names the nested repo the editor will switch to when the file
  lives in another one, and notes that unsaved edits are kept as a draft. A
  newer request replaces an unanswered one. With a base revision the file
  opens in the diff view against it (`Esc` back to editing).
- **open with a note** (`--msg-note <text>`, or `-` for stdin; needs the
  `#L<line>` anchor) also shows the note in a box under the line — right of
  the line numbers, as wide as the note needs up to the pane, word-wrapped,
  with light markdown (`# heading`, `- bullet`, `` `code` ``, `**bold**`,
  fenced blocks, `| pipe | tables |`). The editor stays live: move the cursor and scroll as usual,
  scroll a tall note with `Shift+↑/↓` or the mouse wheel over the box, and press `Esc` to
  close it (it also closes on your first edit). On the wire the note follows
  the path after a line that is exactly `---`.
- **review** opens review mode (after the same confirmation when you are
  busy): the files on the left as a folder tree like the main sidebar, each
  badged **M** / **A** / **D** — click a folder to fold or unfold it (a folded
  one shows how many files it hides); the selected file's diff against the base (`HEAD` by default) on the right, `git diff`
  style — added lines green, removed red, a new file all green, a deleted file
  all red. Files may span several nested repos. Review is read-only (no
  editing, no jump-to-definition), and `Esc` returns you to exactly the file
  and repo you were on.

- **guided session** (`--msg-session <id>` on each step) walks you through
  code one note at a time. The agent sends all its steps up front — each
  command returns at once (`ok step N queued`) and the editor queues them.
  The first step asks **Start session / Decline**; then you walk the queue at
  your own pace: `Enter` shows the next step instantly, `Backspace` goes
  back, `Esc` ends the session. Every step jumps straight to its file — other
  files, other nested repos — without asking; the box is titled
  `<id> · step 2/5`, and the file is read-only while it shows (move, scroll
  and `Shift+↑/↓` the note still work). Past the last queued step the box
  waits for the agent's next one. The agent sends `end` after its last step,
  and `Enter` on that step then finishes the tour. Once you end a session,
  the agent's next step gets `ended …` (exit 3) and it stops;
  `ntee --msg --msg-session <id> wait` blocks until you finish (`ok finished`,
  exit 0) or close it (`ended …`, exit 3).

  ```sh
  ntee --msg --msg-session tour1 --msg-note "flags start here" open cmd/ntee/main.go#L40
  ntee --msg --msg-session tour1 --msg-note "the request goes out" open cmd/ntee/msg.go#L180
  ntee --msg --msg-session tour1 end
  ntee --msg --msg-session tour1 wait   # optional: until you're done
  ```

| Review key | Action |
|---|---|
| `↑/↓` `PgUp/PgDn` `←/→` `Home/End` | move through the diff |
| `Shift+↑/↓` | previous / next file (reopens a folded folder) |
| `n` / `p` | next / previous change |
| `Ctrl+F` | find in this file (removed lines included) |
| `Ctrl+P` | jump to a file of the review |
| `Ctrl+G` | search across the review's files |
| `Esc` | leave the review |

### For coding agents

Paste this into your project's `CLAUDE.md` / `AGENTS.md` so your agent shows
you its work in ntee:

```markdown
## Showing changes in ntee
The user edits in ntee-editor. After changing files, send them for review:
`ntee --msg review <changed files…>` (or pipe `git diff --name-only`).
To point at a specific place: `ntee --msg open <file>#L<line>`; to explain
it, add a note: `ntee --msg --msg-note "<why>" open <file>#L<line>` (markdown
is fine; `--msg-note -` reads a longer note from stdin).
To walk the user through several places, send each as a step of one session,
one after another: `ntee --msg --msg-session <id> --msg-note "<why>" open <file>#L<line>`
(each returns at once; exit 3 means the user ended it — stop sending), then
`ntee --msg --msg-session <id> end`.
Paths are relative to the current directory. The command prints `ok …` on
success; `error … disabled` means the user has not enabled external messages
(Ctrl+T → external) — mention it once and carry on. Run `ntee --msg help`
for details.
```

## Tabs & drafts

Every opened file becomes a tab at the top of the pane (**red = unsaved**).
Switching away from a dirty buffer stashes a draft — content plus up to 15 undo
steps — in ntee-db. Reopening the tab, or relaunching the editor, restores it
exactly, with undo stepping back to the on-disk version. `Ctrl+S` saves and
drops the draft; `Esc` discards it. The tab list and active tab persist per
project.

## Code intelligence (LSP)

Language servers start **lazily** — opening the first file of a language spawns
its server, scoped to the file's nearest project root (so a monorepo frontend
loads its ~300 files, not the repo's 15k). You get:

- **Diagnostics** — colored `●` gutter markers, `✗N ⚠M` status counts, and the
  cursor line's message in the status bar.
- **Autocomplete** — as you type, server-ranked.
- **Definition & references** — `Ctrl+J` / `Ctrl+O`, with UTF-16 column
  bridging done for you.

ntee is **LSP-strict**: when a language has a configured server, its answer is
final — you'll see "still starting…" or "no definition found" rather than a
guessed jump. File types with no server say so (a bare file path under the
cursor still opens directly). A missing binary degrades to a one-time notice.

A crashed server restarts on demand; a rapid crash-loop disables the language
for the session with a note. Check or override any of this live in the
[inspection dashboard](#inspection-mode-ctrlt).

### Installing language servers

```sh
ntee --prepare-lsp                        # all languages: plan, ask, install, write config
ntee --prepare-lsp python ruby            # only the named languages
ntee --prepare-lsp --yes go java          # skip the prompt (flags before names)
```

Built-in recipes: **go** (gopls) · **typescript/js/react** (typescript-language-server) ·
**python** (pyright) · **ruby** (ruby-lsp) · **java** (jdtls) · **kotlin**
(kotlin-language-server) · **vue** (@vue/language-server). Installs use the platform's native tool
(`brew` / `go install` / `npm` / `gem`), skip languages whose runtime is
absent (telling you what to install), keep your tuned config entries, and back
the old file up to `config.yaml.bak`. Recipes can be overridden per language
via an `install:` block.

### Toggling LSP

In-editor: `Ctrl+T`, then `lsp enable ruby`, `lsp disable all`, etc.

From the shell:

```sh
ntee --disable-lsp typescript vue   # write enable: false for these languages
ntee --disable-lsp all              # turn LSP off globally (lsp.enabled: false)
ntee --enable-lsp typescript        # flip a language (or 'all') back on
```

## Configuration

Defaults ← `~/.config/ntee-editor/config.yaml` ← `<project>/.ntee-editor.yaml`:

```yaml
version: 1
editor: { tab_width: 4, language_tab_widths: {}, max_snapshots: 50, max_highlight_kb: 512 }
# a language's own tab size, e.g. language_tab_widths: { go: 8, yaml: 2 } (Ctrl+T → code style)
tree:   { ignore: [".git"] }    # only .git is hidden; .gitignore'd paths show grayed
theme:  { syntax: "gruvbox" }   # any chroma style name (monokai, dracula, …)
languages:
  go:         { extensions: [".go"], lsp: { command: "gopls" } }
  typescript: { extensions: [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"],
                lsp: { command: "typescript-language-server", args: ["--stdio"] } }
  # ruby: { enable: false }     # turn a server off without deleting its config
lsp: { enabled: true }
```

- `languages.<name>.extensions` are **unioned** with the built-in defaults, so
  you can add a file type to an existing server without re-listing them.
- `command` / `args` / `init` overlay the defaults when set (`init:` passes
  LSP initializationOptions — e.g. `tsserver.path` for a globally installed
  typescript ≤5.x; the TS 7 native preview has no `lib/tsserver.js` and won't
  work).
- `enable: true|false` per language (omitted = on).
- The default theme is a tuned gruvbox dark; any chroma style name works,
  rendered truecolor and auto-degraded on 256-color terminals.

### Custom keybindings

Bindings live in the **user** config (`~/.config/ntee-editor/config.yaml`), not
in ntee-db, so copying that one file moves them to another machine. Rebind keys
from `Ctrl+T` → **keybinds**, or edit the `keybinds:` section by hand.

| Action | Default | What it does |
|---|---|---|
| `quit` | `ctrl+q` | quit the editor |
| `repo_picker` | `ctrl+w` | switch the nested repo |
| `goto_file` | `ctrl+p` | goto anything |
| `uncommitted` | `ctrl+u` | uncommitted files |
| `grep` | `ctrl+g` | search the project |
| `inspect` | `ctrl+t` | inspection dashboard |
| `op_mode` | `ctrl+r` | run an op-command |
| `next_tab` | `shift+tab` | cycle open tabs |
| `save` | `ctrl+s` | save the buffer |
| `undo` | `ctrl+z` | undo |
| `redo` | `ctrl+y` | redo |
| `find` | `ctrl+f` | find in file (also "next match" in the find bar) |
| `jump_to_reference` | `ctrl+j` | jump to definition |
| `jump_back` | `ctrl+o` | jump back |
| `expand_selection` | `ctrl+a` | expand the selection |
| `exec` | `ctrl+e` | `@exec` bar (also "replace" from the find bar) |

**By hand** — list only what you change; everything else keeps its default.
For example, goto on `Ctrl+N`, save on `F5`, and undo/redo swapped:

```yaml
keybinds:
  goto_file: ctrl+n
  save: f5
  undo: ctrl+y
  redo: ctrl+z
```

**What the keybinds pane writes** — the same bindings, saved as the full list
(alphabetical, every action spelled out) so the file always says exactly which
key does what:

```yaml
keybinds:
    exec: ctrl+e
    expand_selection: ctrl+a
    find: ctrl+f
    goto_file: ctrl+n
    grep: ctrl+g
    inspect: ctrl+t
    jump_back: ctrl+o
    jump_to_reference: ctrl+j
    next_tab: shift+tab
    op_mode: ctrl+r
    quit: ctrl+q
    redo: ctrl+z
    repo_picker: ctrl+w
    save: f5
    uncommitted: ctrl+u
    undo: ctrl+y
```

Saving from the pane rewrites the whole file, so comments are lost (the
previous version is kept as `config.yaml.bak`).

- A key must be a `ctrl+…` / `alt+…` chord, `f1`–`f12`, or `shift+tab`, and
  each key can belong to one action. `ctrl+c` is reserved (always quits).
- Actions you leave out keep their defaults. A bad entry (unknown action,
  invalid or duplicate key) keeps the default too and is reported in the
  status bar at startup.
- Keybinds in a project's `.ntee-editor.yaml` are ignored, so a repo can never
  remap your keys.
- Arrows, `Enter`, `Esc`, `Tab` and the overlays' own keys are not rebindable.

## Persistence & maintenance

Each project gets its own ntee-db store under
`~/.ntee-editor/stores/<hash(project-root)>/` holding recent files, undo
snapshots, drafts, tabs, and the session. ntee-db is single-writer (flock):
opening the same project twice falls back to in-memory state with a notice —
undo still works, nothing persists.

Old undo versions auto-evict per file, but dead space accumulates in the log
over time. Press `Ctrl+T` to see exactly how much, and `db compact` /
`db relieve` to reclaim it — no external tools needed.

## Architecture notes

- `internal/view`, `internal/input`, `internal/filetree`, `internal/fuzzy` are
  pure (no Bubble Tea) and unit-tested; `internal/app` holds the single Model
  with per-mode key handlers (`keys_edit.go`, `keys_search.go`, …).
- Highlighting is whole-buffer chroma tokenization cached per line, refreshed
  at edit-burst boundaries. The search view re-tokenizes its frozen snapshot so
  matches (and replace previews) overlay syntax colors.
- Undo is full-content snapshots keyed `versions:<seq>` in ntee-db, with a
  `file` secondary index whose `MaxPerValue` auto-evicts the oldest per file.
- `internal/lsp` is a hand-rolled stdio JSON-RPC client (Content-Length
  framing, ordered notifications), one server per language, with a bridge
  mechanism for hybrid servers (Vue → TypeScript).
- Mouse hit-testing shares its layout math with the renderer (single
  `sidebarWidth()` source of truth), so clicks can't drift from what's drawn.

## License

Apache License 2.0 — see [LICENSE](LICENSE). Copyright 2026 Nick An.

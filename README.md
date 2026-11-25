<div align="center">

```
███╗   ██╗███████╗██╗    ██╗   ████████╗██╗   ██╗██████╗ ███████╗
████╗  ██║██╔════╝██║    ██║   ╚══██╔══╝╚██╗ ██╔╝██╔══██╗██╔════╝
██╔██╗ ██║█████╗  ██║ █╗ ██║      ██║    ╚████╔╝ ██████╔╝█████╗
██║╚██╗██║██╔══╝  ██║███╗██║      ██║     ╚██╔╝  ██╔═══╝ ██╔══╝
██║ ╚████║███████╗╚███╔███╔╝      ██║      ██║   ██║     ███████╗
╚═╝  ╚═══╝╚══════╝ ╚══╝╚══╝       ╚═╝      ╚═╝   ╚═╝     ╚══════╝

          ███████╗██████╗ ██╗████████╗ ██████╗ ██████╗
          ██╔════╝██╔══██╗██║╚══██╔══╝██╔═══██╗██╔══██╗
          █████╗  ██║  ██║██║   ██║   ██║   ██║██████╔╝
          ██╔══╝  ██║  ██║██║   ██║   ██║   ██║██╔══██╗
          ███████╗██████╔╝██║   ██║   ╚██████╔╝██║  ██║
          ╚══════╝╚═════╝ ╚═╝   ╚═╝    ╚═════╝ ╚═╝  ╚═╝
```

**ntee** */en·tee/*: say the letters **N** and **T**, for **N**ew **T**ype **E**ditor.

### The terminal code editor your AI agent works *through*, not around.

Your agent stages its edits in ntee instead of writing them to disk. It tests them in a sandbox,
then walks you through the diff. **Nothing touches your files until you press `a`.**

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.10%2B-00ADD8.svg?logo=go&logoColor=white)](go.mod)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey.svg)
[![MCP](https://img.shields.io/badge/MCP-ready-8A2BE2.svg)](#-built-for-coding-agents)

[Install](#-install) · [Agent setup](#-connect-your-agent-in-60-seconds) · [Why ntee](#-why-ntee) · [Keys](#%EF%B8%8F-keys-youll-actually-remember) · [Reference](#reference)

</div>

```
┌ ntee-editor · ~/my-app ─────────────── EDITS · agent session mcp-1 · ✎ agent editing… ┐
│ ↓ internal/            │ @@ -40,6 +40,9 @@ func Load(path string) (*Config, error) {  │
│  ↓ app/                │  40   data, err := os.ReadFile(path)                         │
│    A cache.go       +64│  41 - if err != nil { return nil, err }                      │
│    M app.go ◆    +12 −4│  41 + if errors.Is(err, fs.ErrNotExist) {                    │
│  ↓ config/             │  42 +     return Defaults(), nil                             │
│    M config.go ◆  +9 −2│  43 + }                                                      │
│                        │       ┌ note 1/1 · L41 ───────────────────────┐              │
│                        │       │ A missing config file is not an error │              │
│                        │       │ any more: fall back to the defaults.  │              │
│                        │       └───────────────────────────────────────┘              │
│                        │  44   return parse(data)                                     │
│                        │                                                              │
│                        │                 ┌ decide ───────────────────────────────────┐│
│                        │                 │ ◉ AUTO-DRIVE ON ●    ✓ check: go test ok  ││
│                        │                 │ a apply · x discard · r request changes   ││
│                        │                 └───────────────────────────────────────────┘│
└────────────────────────┴──────────────────────────────────────────────────────────────┘
 @review > internal/config/config.go                   n/p change · ]/[ note · Esc leave
```

---

## ✨ Why ntee?

<table>
<tr>
<td width="50%" valign="top">

### 🤖 Fully driven by your agent
ntee ships an **MCP server** (`ntee --mcp`). Your agent reads files, stages
edits, shows you code with notes, and gives you guided walkthroughs, all inside
the editor you already have open. Several agents can work on one change set
without overwriting each other.

</td>
<td width="50%" valign="top">

### 🛡️ No more surprise edits
Agent edits are **staged**, not written. A file with staged edits is
**read-only** in your editor (`✎` in the tree), so you can't type over it by
accident. Review, blame, diff and preview views are read-only too, and the
editor asks before it moves you anywhere.

</td>
</tr>
<tr>
<td width="50%" valign="top">

### 🧪 Sandbox before disk
When the agent finishes, ntee builds a **sandbox**: a copy of every repo the
change touches, with the staged edits in place (an instant APFS clone on
macOS). The agent runs its build and tests there and reports **pass / fail**,
which you see before you decide.

</td>
<td width="50%" valign="top">

### 👀 Review like a pull request, locally
Every agent change opens as a **live, multi-file, multi-repo diff**. It grows as
the agent types, and auto-drive follows the agent's newest change. Then press
`a` to apply, `x` to discard or `r` to request changes. You can even say
**Apply & commit** or **Apply & new PR** and the agent does the git work.

</td>
</tr>
<tr>
<td width="50%" valign="top">

### ⚡ A real editor, not a viewer
Sublime-style fuzzy finder, repo-wide grep, LSP autocomplete / diagnostics /
go-to-definition for 7 language families, find & replace with live preview,
git diff / blame / conflict solver, OpenAPI & GraphQL previews.

</td>
<td width="50%" valign="top">

### 💾 Nothing is ever lost
Tabs, unsaved drafts, undo history and your place in every repo are stored
per project in [ntee-db](https://github.com/nickooan/ntee-db) and come back
when you relaunch. Every agent session is kept as a record you can trace
later.

</td>
</tr>
</table>

---

## 📦 Install

```sh
brew install --cask nickooan/tap/ntee          # macOS: universal binary (Intel + Apple Silicon)
go install github.com/nickooan/ntee-editor/cmd/ntee@latest   # Linux or from source (Go 1.26.10+)
ntee --prepare-lsp                             # optional: install language servers in one go
```

Update with `brew upgrade --cask ntee`. With `go install` the binary lands in
`~/go/bin`, so make sure that folder is on your PATH.

```sh
ntee                  # open the current directory
ntee <path>           # open a project
ntee src/app.go#L42   # open a file with the cursor on line 42
ntee notes.txt        # a file that doesn't exist yet opens empty; Ctrl+S creates it
```

## 🔌 Connect your agent in 60 seconds

**1. Let the editor take requests.** In ntee press `Ctrl+T` → **external** →
**accept messages** (or type `ext on`). It's off by default, and it only
listens on a private per-project unix socket.

**2. Give your agent the tools.** For Claude Code:

```sh
claude mcp add ntee -- ntee --mcp
```

Any MCP client works the same way: run `ntee --mcp` on stdio from the project
folder.

**3. (Optional) Block the agent's own file tools on staged files.** Add this to
the project's `.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Read|Edit|MultiEdit|Write", "hooks": [{ "type": "command", "command": "ntee --hook" }] }
    ]
  }
}
```

That's all. Ask your agent for a change and watch it show up in ntee.

### What happens when your agent edits

```
 agent                         ntee editor                                you
 ─────                         ───────────                                ───
 read_file ──────────────────▶ latest content (staged edits included)
 edit_replace / edit_write ──▶ staged in a session, disk untouched  ──▶  live diff opens,
                                                                         auto-drive follows
 edit_end ───────────────────▶ session finalises
 edit_sandbox ───────────────▶ scratch copy of every touched repo
   go test / npm test …  (in the sandbox)
 edit_check pass|fail ───────▶ check shown in the review            ──▶  a  apply all
                                                                         x  discard all
                                                                         r  request changes
 edit_wait ◀───────────────── outcome (+ "commit" / "open a PR")   ◀──  Apply & commit / new PR
```

- **Applying never overwrites your work.** A file that changed on disk after it
  was staged is left alone and reported. Unsaved buffers and drafts are skipped,
  or replaced only if you say so, and the replaced text stays in undo.
- **Choose your policy** in `Ctrl+T` → **agent edits**: **review first**
  (default) or **agent applies** (written at once when the checks pass).
- **Changed your mind later?** **Keep for later** leaves the edits staged. Every
  session, finished or not, is listed in `Ctrl+T` → agent edits and can be
  opened as a trace.

---

## 🤖 Built for coding agents

| Tool (MCP) | What it does |
|---|---|
| `read_file` | Read a file's **latest** content, including edits any agent has staged but not yet written |
| `edit_replace` · `edit_write` | Stage an exact-text replacement or a whole file (new files too). Edits stack, and a stale read is caught automatically |
| `edit_end` · `edit_sandbox` · `edit_check` | Finish, build a sandbox, report the build/test result |
| `edit_wait` · `edit_status` · `edit_discard` · `edit_report` | Hear your decision, inspect the session, drop it, report back |
| `show_code` | Open a file at a line with a markdown **note box** beside it, instead of pasting code into the chat |
| `walkthrough_step` · `walkthrough_end` · `walkthrough_wait` | A guided **code tour**: one note per stop, you walk it at your own pace |

**Multi-agent aware.** Two agents editing the same file don't clobber each
other. Each one reads the other's staged edits, stacks on top of them, and is
told when someone changed the lines it was about to touch. Sessions can be
shared, and the review opens once every agent is done.

**No MCP? Use the CLI.** The same features are available from any script, git
hook or agent through `ntee --msg`:

```sh
ntee --msg open internal/app/app.go#L120                      # jump the editor to a line
ntee --msg --msg-note "why this matters" open app.go#L120      # …with a note beside it
git diff --name-only | ntee --msg review                       # multi-file diff review
ntee --msg --msg-review-session impl-1 review internal/app/app.go   # live review that grows
ntee --msg --msg-session tour1 --msg-note "start here" open cmd/ntee/main.go#L40   # guided tour
ntee --msg help                                                # the full guide
```

## 🛡️ Safe by default

| You worry about… | ntee's answer |
|---|---|
| Typing over a file the agent is still changing | Files with staged edits are **read-only** (`read-only · staged agent edits`), and the lock lifts when the edits are applied or discarded |
| The agent's change breaking the build | It must **build and test in a sandbox** first, and you see the result before you decide |
| Changes landing while you're mid-thought | A busy editor **asks** (**Jump / Ignore**), and auto-opened reviews wait until you've stopped typing for 1.5 s |
| Losing unsaved work | Drafts survive tab switches and relaunch. Applying skips unsaved files unless you choose to replace them, and undo brings your text back |
| Deleting the wrong thing | `:rm` always confirms, and discarding an agent session asks first |
| A cloned repo hijacking your editor | Project config can never name executables or remap your keys, and every path is jailed to the project folder |
| Not knowing what an agent did last week | Every edit session is saved as one record (before/after content, every edit, who sent it, the outcome) |

## ⌨️ Keys you'll actually remember

| Key | Action | | Key | Action |
|---|---|---|---|---|
| `Ctrl+P` | goto file (fuzzy) | | `Ctrl+S` | save |
| `Ctrl+G` | grep the repo | | `Ctrl+Z` / `Ctrl+Y` | undo / redo (kept across relaunch) |
| `Ctrl+U` | goto **uncommitted** file | | `Ctrl+F` | find & replace, live preview |
| `Ctrl+W` | switch nested repo | | `Ctrl+J` | jump to definition / references |
| `Ctrl+R` | run a saved shell command | | `Ctrl+O` | jump back |
| `Ctrl+T` | dashboard: LSP, keys, agents… | | `Ctrl+A` | grow selection: word → line |
| `Shift+Tab` | next tab | | `Ctrl+E` | `@exec` bar: copy, git diff/blame, … |
| `Esc` | step back out, one layer at a time | | **click** / **Ctrl+click** | move cursor / go to definition |

In a review: `n`/`p` next/previous change · `]`/`[` next/previous note ·
`Shift+↑/↓` file · `{`/`}` repo · ``Ctrl+` `` auto-drive · `a` `x` `r` decide.

Every chord is **rebindable** from `Ctrl+T` → **keybinds**: press the action,
press the new key. Bindings are saved to one portable YAML file.

**Your first five minutes:**
1. **Type to navigate.** The `@query >` bar drives the sidebar: type a path
   fragment, directories expand, `Enter` opens the file in edit mode.
2. **`Ctrl+P` finds files, `Ctrl+G` greps contents**, anywhere, any time.
3. **Click to place the cursor**, type to edit, `Ctrl+S` to save.
4. **`Esc` walks you back out**: it clears the selection, then leaves edit mode,
   then goes up a directory.
5. **`Ctrl+Q` quits.** Your tabs, drafts, and undo history will be there when
   you come back.

---

## Reference

- [Keymap](#keymap) · [Find & replace](#find--replace-ctrlf) · [Exec bar](#exec-bar-ctrle) · [Command bar](#command-bar-)
- [Inspection mode](#inspection-mode-ctrlt) · [Op-commands](#op-commands-ctrlr)
- [External messages & review](#external-messages--review) · [Agent edit sessions](#agent-edit-sessions) · [For coding agents](#for-coding-agents)
- [Tabs & drafts](#tabs--drafts) · [Code intelligence (LSP)](#code-intelligence-lsp) · [Configuration](#configuration) · [Custom keybindings](#custom-keybindings) · [Persistence](#persistence--maintenance)

## Keymap

The keys below are the defaults. Every `Ctrl`/`Shift+Tab` action under
**Everywhere** and the edit-mode chords (save, undo, redo, find, jump/back,
select, exec) can be rebound (see [Custom keybindings](#custom-keybindings)).

### Everywhere

| Key | Action |
|---|---|
| `Ctrl+P` | **Goto file**: fuzzy finder; an empty query lists recent files. Append `#L<line>` (e.g. `build.sh#L8`) to open on that line |
| `Ctrl+U` | Goto **uncommitted** file: the same finder, limited to git-dirty paths |
| `Ctrl+W` | **Workspace repo**: re-root the editor at a nested git repo under the opened directory (pick the top `name/` row to go back to the whole workspace). The file tree, query bar, `Ctrl+P`/`Ctrl+U`/`Ctrl+G`, change highlights, and git views then cover that repo only, as if you had opened it directly; the title bar shows `working repo: …` in orange next to the path. Each repo remembers its own tabs, open file, and tree position (also across relaunch), and switching stashes unsaved edits as a draft. Refused when the opened directory is already a git repo |
| `Ctrl+G` | **Grep the repo**: colored preview on top, results below; `↑/↓` + `Enter` jumps |
| `Ctrl+T` | **Inspection dashboard**: store stats, LSP control, op-commands, keybinds, code style, external messages, agent edits ([below](#inspection-mode-ctrlt)) |
| `Ctrl+R` | **Run an op-command** on the open file: pick a saved command, fill its `{$1}` `{$2}` args, watch the output stream ([below](#op-commands-ctrlr)) |
| `Shift+Tab` | Cycle the focused tab (wraps) |
| `Ctrl+Q` / `Ctrl+C` | Quit (session saved). `Ctrl+C` always quits, whatever the bindings |

### Query bar (home)

| Key | Action |
|---|---|
| type a path / fragment | expand directories · highlight the best match · suggest completions |
| `↑/↓` (popup open) | move the popup selection; it previews in the bar and highlights in the sidebar |
| `↑/↓` (no popup) | scroll the open file |
| `Shift+↑/↓` | walk the sidebar tree row by row (never expands) |
| `Enter` | directory → enter it · file → open in edit mode · `:cmd` → run command |
| `path#L<line>` | optional line anchor, GitHub style (`scripts/build.sh#L8`, `#l8`, `#L8-L12`). Matching ignores it; `Enter` opens the file on that line (past the end → last line) |
| `Tab` | edit the currently open file |
| `Ctrl+F` | find in the open file |
| `Esc` | go up one directory |

### Edit mode

| Key | Action |
|---|---|
| **mouse click** | move the cursor to the clicked position (gutter click → column 0). Hold `Shift` for native terminal text selection |
| **`Ctrl`+click** | jump to definition at the clicked token (same as `Ctrl+J`). Some terminals capture Ctrl+click as a right-click; use `Ctrl+J` there |
| **wheel / two-finger scroll** | scroll up/down (moves the cursor); horizontal scroll does nothing |
| `Ctrl+S` | save (also clears the file's stashed draft) |
| `Ctrl+Z` / `Ctrl+Y` | undo / redo: snapshot bursts, persisted across relaunch |
| `Ctrl+A` | progressive select: word → line; then `Shift+↑/↓` extends line-wise |
| `Ctrl+F` | find in file ([below](#find--replace-ctrlf)) |
| `Ctrl+J` | **jump to definition** (or the file path under the cursor); on a definition line it finds **references** instead. Multiple hits open a picker with a live code preview |
| `Ctrl+O` | jump back (restores file, cursor, scroll; 20-deep trail) |
| `Ctrl+E` | open the `@exec >` command bar ([below](#exec-bar-ctrle)) |
| `PgUp` / `PgDn` | page with a one-line overlap |
| `Home` / `End` | line start / end |
| `Esc` | clear selection → then discard unsaved edits and return to the query bar |

**Autocomplete** pops up on its own as you type an identifier or `.`:
`↑/↓` selects, `Tab`/`Enter` accepts, keep typing to filter, `Esc` dismisses.
Powered by the file's language server.

### Find & replace (`Ctrl+F`)

Type to search; matches highlight live (case-insensitive; regex supported,
falling back to literal). `↑/↓` cycles the focused match (orange), `Enter`
jumps the cursor to it, `Esc` backs out.

Press **`Ctrl+E` while matches are highlighted** to enter the replace bar:

| Command | Action |
|---|---|
| `c <text>` | replace the **focused** match; focus then lands on the next one, so you can `c` again |
| `mlc <text>` | **m**ulti-**l**ine **c**ursor: replace **all** matches at once |

As you type the replacement, the buffer shows a **live preview**: target spans
render in green exactly as they'll look after `Enter`. Only the matched span is
touched (`search54321` + `c search123` → `search12354321`); a bare `c`/`mlc`
deletes the match. Every replace, even `mlc` across a hundred lines, is one
`Ctrl+Z` step.

### Exec bar (`Ctrl+E`)

Editor commands with Tab-completed suggestions:

| Command | Action |
|---|---|
| `copy` (`cp`) `[a-b\|all\|fpath]` | copy the selection, a line range, the whole buffer, or the file's path (relative to its git repo when it is in one) |
| `cpfp` / `cpafp` | copy the file's repo-relative / absolute path |
| `cpfpl` | copy the repo-relative path with the cursor line, e.g. `scripts/build.sh#L8`; paste it into the query bar or `Ctrl+P` to come back here |
| `jump` (`jp`) `<line\|top\|end>` | go to a line (lands ~30% from the top) |
| `git scf` | **s**olve **c**on**f**lict: review the buffer's conflicts interactively, with ours tinted green, theirs blue and markers yellow. `↑/↓` move line by line, `Shift+↑/↓` jump between conflict blocks; on a marker line a picker pops up: `←/→` choose **Use HEAD / Use \<branch\> / Use both**, `Enter` applies (each apply is one `Ctrl+Z` step), `Esc` goes back to editing at the cursor. Applied resolutions stay, untouched conflicts remain |
| `git diff [rev]` | review the file's uncommitted changes (or its diff against `rev`) in a read-only GitHub-style view: added lines green with a `+` gutter marker, removed lines red with `-` (no number). `↑/↓`/`PgUp`/`PgDn` review, `Shift+↑/↓` jump between change hunks, `Ctrl+J` jump to definition (`Ctrl+O` returns to the review), `Esc` back to editing at the reviewed line. In a workspace the file's own repo is used |
| `git blame` | annotate every line with its author and commit date in a read-only view; the gutter shows `author date │` instead of line numbers, and unsaved or uncommitted lines show a dimmed `uncommitted` placeholder. `↑/↓`/`PgUp`/`PgDn` move, `Shift+↑/↓` jump between commit groups, `Ctrl+J` jump to definition (`Ctrl+O` returns to the blame), `Esc` back to editing at the cursor line. In a workspace the file's own repo is used |
| `openapi` / `graphql` | a read-only rendered preview of an OpenAPI v3 spec (cross-file `$ref`s resolved) or the project's GraphQL schema, with an outline and in-preview search; `Esc` jumps back to the source of the row under the cursor |
| `tab <name\|cl\|cr>` | switch tab / close-left / close-right |

### Command bar (`:`)

`jump <line|top|end>` (alias `jp`) · `tab <name|cl|cr>` · `revert` (restore
the last saved snapshot) · `refresh` (re-scan the file tree). In the query bar,
`<path> :mkdir <rel>`, `<path> :touch <rel>` and `<path> :rm` create and delete
files and folders; `:rm` always asks for confirmation.

### Inspection mode (`Ctrl+T`)

A dashboard for the editor's own machinery. `Shift+↑/↓` switches the left menu;
`Esc` returns to where you were.

- **ntee-db**: the project store's disk usage: live records, main-log size
  with dead-space percentage, blob usage, orphaned bytes.
- **lsp**: every configured language server with its live status:
  **running** (green) · **stopped** (yellow, starts on demand) ·
  **disabled** (gray, with the reason). Press `→` to move into the list:
  `↑/↓` picks a language and `Enter` opens the actions that fit its state.
  **start / stop / restart** act on this session only and leave your config
  alone. A stopped server starts again the next time a file needs it.
  **enable / disable** also save `enable: true` / `false` to your config, so
  they last across restarts. `↑/↓` chooses, `Enter` applies, `Esc` closes the
  dropdown, then goes back.
- **system**: version and syntax color style. Press `→` to move into the
  style list: `↑/↓` browses with a live code preview in that style, `Enter`
  applies it (saved to your config), `←`/`Esc` goes back.
- **op-commands**: your saved shell command templates as a KEY / TYPE /
  COMMAND table. Press `→` to move into the table; there `↑/↓` picks a row,
  `Enter` edits it (or adds a new one on `+ New command`), `Tab`/`Shift+Tab`
  moves between key, type and command, `Ctrl+S` saves, `Esc` reverts the
  edit, and `d` deletes after a `y/n` confirmation. `←`/`Esc` goes back to the
  menu. TYPE is the file types the command is for, e.g. `go`, `tf, tfvars`, or
  `*` for every file (left empty it saves as `*`). Typing a type suggests common
  ones (`go`, `rb`, `sh`, `tfvars`, …, the open file's type first). One key can
  exist once per type, so `test` can be `go test ./...` for `go` and
  `bundle exec rspec {$fpath}` for `rb`. A key whose types clash with an
  existing command turns red as you type, and saving it pops up an alert.
  Typing `{` or `$` in a command opens placeholder suggestions: the next free
  argument (`{$2}` when the command already has `{$1}`), the existing ones, and
  `{$fpath}`/`{$dpath}`. `↑/↓` chooses, `Tab`/`Enter` inserts, `Esc` closes the
  menu. Placeholders are colour-coded in the table: arguments purple, built-ins
  blue, `{#"…"}` fragments green (with their `$n` purple), broken ones red.
- **keybinds**: every rebindable action with its key. Press `→` to move into
  the list: `↑/↓` picks an action, `Enter` then **press the new chord** to bind
  it (`Esc` cancels), `r` resets that action to its default, `R` (or `Enter` on
  the last row) resets **all** of them after a `y/n` confirmation. A key that's
  already taken, or isn't a chord, is refused in the status bar. Custom keys
  are highlighted next to their default. Changes apply immediately and are
  saved to your config (see [Custom keybindings](#custom-keybindings)).
- **code style**: the tab size, as a LANGUAGE | TAB SIZE | DESCRIPTION table
  laid out like keybinds. **all files** is the size for everything; each
  language (go, typescript, python, yaml, markdown, makefile, …) uses it until
  you give that language its own. The size is how many spaces `Tab` inserts
  *and* how wide tab characters in a file are drawn: in the editor, diffs,
  review, previews and notes. Press `→` to move in: `↑/↓` picks a row, `Enter`
  types a size (1–16, `Enter` saves, `Esc` cancels), `+`/`-` step it, `r` lets
  a language use **all files** again (or puts **all files** back to 4), `R`
  resets everything after a `y/n`. In the bar: `tab 2`, `tab go 8`,
  `tab go default`, `tab reset`. Saved to your config as `tab_width` and
  `language_tab_widths`. A size set by the project's `.ntee-editor.yaml` is
  tagged **project** and can only be changed in that file.
- **external**: whether other programs may send this editor requests
  ([External messages & review](#external-messages--review)). Shows the
  socket the editor listens on, the **accept messages** switch (off by
  default), whether **agent sessions** ask before they start or start
  directly, the **history limit** (how many recent messages each project
  keeps, 1–100, default 10), and how many this project has saved. Press `→`
  to move in: `↑/↓` picks a row, `Enter` toggles a switch, edits the limit
  (type a number, `Enter` saves, `Esc` cancels), or opens the **history**:
  every saved message on the left, its details (including the **git hash**
  it compares against, `HEAD` by default) and raw text on the right,
  `Enter` to replay one, `Esc` back. `→` moves focus to the details so `↑/↓`
  (or the wheel) scroll a long path list; `←` or `Esc` returns to the list.
- **agent edits**: what happens when an agent ends an edit session
  (**review first**, the default, or **agent applies**), how many finished
  sessions each project keeps (1–100, default 20), and every session, pending
  first. `Enter` on a pending one opens its live review, on a finished one its
  read-only trace; `a` / `x` apply / discard, `r` requests changes, `f` stops
  waiting for agents still editing or checking ([Agent edit sessions](#agent-edit-sessions)).

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
| `ext sessions ask\|direct` | agent sessions ask before they start, or start directly |
| `ext limit <n>` | keep the latest `n` messages per project (1–100) |
| `ext history` | open the external-message history |
| `edits review\|apply` | agent edit sessions open for review first, or the agent applies them |
| `edits keep <n>` | keep the latest `n` finished edit sessions per project (1–100) |
| `edits discard <session>` | discard a pending edit session |

LSP commands act live *and* write `~/.config/ntee-editor/config.yaml` (the
previous file is backed up to `config.yaml.bak`), so the change sticks across
restarts. If a server fails to start (e.g. binary missing), the error shows
right in the bar.

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
resulting command before you press `Enter`. Commands without arguments run
straight away. Values are shell-quoted only when they need it, so `master` is
passed as `master` and `my file.tf` as `'my file.tf'`. The program gets the
same arguments either way. Because of that, write placeholders bare:
`git commit -m {$1}`, not `-m "{$1}"`. A placeholder inside quotes (or right
after `\` or `$`) is rejected when you save, since the quoting would no longer
protect it. Both inputs edit like a text field: `←`/`→` move the cursor and
typing inserts at it, so you can fix a typo in the name or an argument without
retyping the line.

Commands run on the file that is open in the editor (the active tab), which the
overlay names as `file …`, not on whatever the file tree highlights. They read
the file as saved on disk, so if it has unsaved changes the overlay warns you:
save with `Ctrl+S` first if the command should see them.

The command runs with `sh -c` in the workspace directory (the one the editor
was opened on, even after `Ctrl+W` switched to a nested repo), so `{$fpath}` and
`{$dpath}` resolve from there, and its output (stdout and stderr) streams into
the overlay as it arrives. When the process exits, the footer turns green
(`✓ finished`), or red with the exit status if it failed. Press `Esc` to close.
`Esc` while the command is still running kills it. `http(s)://` links in the
output are underlined; click one, even while the command is still running, to
open it in your default browser.

Op-commands are stored in `~/.ntee-editor/global/` and shared by every project.

---

## External messages & review

A running editor can take requests from other programs: your coding agent,
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
editor's folder. It prints one reply line, `ok …` or `error …` (exit code 1).

Either request may name a **base revision** with `--msg-base <rev>` (a commit
hash, tag, branch, `HEAD~2`; on the wire it follows the command on the first
line, `open <rev>` / `review <rev>`). It is always compared with the current
files on disk, uncommitted changes included; without one the base is `HEAD`.
The history saves it and shows it on the message's **git hash** row, and a
replay compares against the same revision.

- **open** jumps straight there from the query view. If you are busy (editing,
  in a bar, a diff, a preview), a dialog asks first (**Jump** / **Ignore**),
  names the nested repo the editor will switch to when the file lives in
  another one, and notes that unsaved edits are kept as a draft. A newer
  request replaces an unanswered one. With a base revision the file opens in
  the diff view against it (`Esc` back to editing).
- **open with a note** (`--msg-note <text>`, or `-` for stdin; needs the
  `#L<line>` anchor) also shows the note in a box under the line: right of
  the line numbers, as wide as the note needs up to the pane, word-wrapped,
  with light markdown (`# heading`, `- bullet`, `` `code` ``, `**bold**`,
  fenced blocks, `| pipe | tables |`). The editor stays live: move the cursor
  and scroll as usual, scroll a tall note with `Shift+↑/↓` or the mouse wheel
  over the box, and press `Esc` to close it (it also closes on your first
  edit). On the wire the note follows the path after a line that is exactly
  `---`.
- **review** opens review mode (after the same confirmation when you are
  busy): the files on the left as a folder tree like the main sidebar, each
  badged **M** / **A** / **D**. Click a folder to fold or unfold it (a folded
  one shows how many files it hides). The selected file's diff against the
  base (`HEAD` by default) is on the right, `git diff` style: added lines
  green, removed red, a new file all green, a deleted file all red. Files may
  span several nested repos. Review is read-only (no editing, no
  jump-to-definition), and `Esc` returns you to exactly the file and repo you
  were on.
- **live review session** (`--msg-review-session <id>`, one file per
  request with an optional note) is a review the agent grows while it works:
  new files slide into the list, changed ones re-diff in place with a short
  animation, and a file can carry several notes (`]`/`[` steps through them).
  `Esc` asks **Quit / Stay**; once you quit, the agent's next send gets
  `ended …` (exit 3).
- **guided session** (`--msg-session <id>` on each step) walks you through
  code one note at a time. The agent sends all its steps up front; each
  command returns at once (`ok step N queued`) and the editor queues them.
  The first step asks **Start tour / Decline** (or starts directly, if you
  chose that in `Ctrl+T` → external); then you walk the queue at your own
  pace: `Enter` shows the next step instantly, `Backspace` goes back, `Esc`
  ends the session. Every step jumps straight to its file (other files, other
  nested repos) without asking; the box is titled `<id> · step 2/5`, and the
  file is read-only while it shows (move, scroll and `Shift+↑/↓` the note
  still work). Past the last queued step the box waits for the agent's next
  one. The agent sends `end` after its last step, and `Enter` on that step
  then finishes the tour. Once you end a session, the agent's next step gets
  `ended …` (exit 3) and it stops; `ntee --msg --msg-session <id> wait`
  blocks until you finish (`ok finished`, exit 0) or close it (`ended …`,
  exit 3). `--msg-diff-session <id>` works the same way, with each step shown
  in the diff view against `--msg-base`.

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
| `{` / `}` | previous / next repo |
| `n` / `p` | next / previous change |
| `]` / `[` | next / previous note of the file |
| `Ctrl+F` | find in this file (removed lines included) |
| `Ctrl+P` | jump to a file of the review |
| `Ctrl+G` | search across the review's files |
| ``Ctrl+` `` | **auto-drive** on/off in a live review (a review session, or an agent's staged edits) |
| `a` / `x` / `r` | agent edit review: apply all / discard all / request changes |
| `Esc` | leave the review |

In a live review, **auto-drive** follows the agent: each file it sends or edits
opens as it arrives, and the cursor lands on the change it just made. The
moment you move (arrow keys, `n`/`p`, `Shift+↑/↓`, a click, the wheel, a
search), auto-drive pauses and your view stays put, even as the agent keeps
editing the file you are reading. The panel at the right of the code shows the
switch (`◉ AUTO-DRIVE ON` / `○ AUTO-DRIVE OFF`); ``Ctrl+` `` jumps back to the
agent's newest change and follows again.

## Agent edit sessions

An agent can **stage** its edits in the editor instead of writing files:
through the MCP tools (`ntee --mcp`, see [Connect your agent](#-connect-your-agent-in-60-seconds))
or `ntee --msg --msg-edit-session <id> write|replace|end|wait …` (`ntee --msg help`
has the full syntax). While a session is open:

- Files with staged edits are marked `✎` in the tree. Opening one says the
  content is **not on disk yet**, and the file is **read-only**: moving,
  selecting, copying, finding and jumping work, but typing, saving and undo
  are refused. A buffer you were already editing stays editable.
- The header shows `✎ agent editing…` (or `⚙ agents checking…`); click it to
  open the session.
- Under **review first**, the session's live review opens as soon as the agent
  starts, from the plain edit or query view only and after you pause typing.
  Elsewhere you get a notice instead. `a` / `x` / `r` stay locked
  (`a x r unlock when done`) until every agent has ended and reported its
  sandbox check.
- When an agent ends, the session **finalises**: the editor builds a sandbox
  (a copy of every touched repo with the staged files in place), each agent
  builds and tests there and reports pass / fail / skip, and then the session
  is yours. A fix from the agent sends it back to editing, and the checks run
  again.
- **Apply** (`a`, or **Apply all** from the `Esc` prompt) asks for a git
  follow-up when every file is in a git repo: **Apply only / Apply & commit /
  Apply & new PR / Cancel**. The editor never runs git itself; the agent hears
  the request with the outcome and does the work (for a PR it agrees the
  branch name with you first).
- **Request changes** (`r`) sends the session back to its agents: they stop,
  ask you what you want in their terminal, then stage the follow-ups in the
  same session.
- **Keep for later** (from the `Esc` prompt, or **Later** on the ready prompt)
  leaves everything staged; reopen it from `Ctrl+T` → agent edits.
- A file that changed on disk after it was staged is never overwritten; it is
  reported as not applied. Several sessions may stage the same file and they
  stack: each builds on the latest staged copy, and applying the top one says
  it writes both (**Apply both**).

Every session is stored as one record (each file before and after, every edit
and which agent sent it, the checks and the outcome) and stays listed in
`Ctrl+T` → agent edits, where a finished one opens as a read-only trace.

### For coding agents

With the MCP server set up, the server's own instructions teach the agent the
whole flow, so there is nothing to paste. Without MCP, paste this into your
project's `CLAUDE.md` / `AGENTS.md` so your agent shows you its work in ntee:

```markdown
## Showing changes in ntee
The user edits in ntee-editor. After changing files, send them for review:
`ntee --msg review <changed files…>` (or pipe `git diff --name-only`).
To point at a specific place: `ntee --msg open <file>#L<line>`; to explain
it, add a note: `ntee --msg --msg-note "<why>" open <file>#L<line>` (markdown
is fine; `--msg-note -` reads a longer note from stdin).
To walk the user through several places, send each as a step of one session,
one after another: `ntee --msg --msg-session <id> --msg-note "<why>" open <file>#L<line>`
(each returns at once; exit 3 means the user ended it, so stop sending), then
`ntee --msg --msg-session <id> end`.
Paths are relative to the current directory. The command prints `ok …` on
success; `error … disabled` means the user has not enabled external messages
(Ctrl+T → external), so mention it once and carry on. Run `ntee --msg help`
for details.
```

## Tabs & drafts

Every opened file becomes a tab at the top of the pane (**red = unsaved**).
Switching away from a dirty buffer stashes a draft (content plus up to 15 undo
steps) in ntee-db. Reopening the tab, or relaunching the editor, restores it
exactly, with undo stepping back to the on-disk version. `Ctrl+S` saves and
drops the draft; `Esc` discards it. The tab list and active tab persist per
project.

## Code intelligence (LSP)

Language servers start **lazily**: opening the first file of a language spawns
its server, scoped to the file's nearest project root (so a monorepo frontend
loads its ~300 files, not the repo's 15k). You get:

- **Diagnostics**: colored `●` gutter markers, `✗N ⚠M` status counts, and the
  cursor line's message in the status bar.
- **Autocomplete** as you type, server-ranked.
- **Definition & references**: `Ctrl+J` / `Ctrl+O`, with UTF-16 column
  bridging done for you.

ntee is **LSP-strict**: when a language has a configured server, its answer is
final. You'll see "still starting…" or "no definition found" rather than a
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
(kotlin-language-server) · **vue** (@vue/language-server). Installs use the
platform's native tool (`brew` / `go install` / `npm` / `gem`), skip languages
whose runtime is absent (telling you what to install), keep your tuned config
entries, and back the old file up to `config.yaml.bak`. Recipes can be
overridden per language via an `install:` block.

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
  LSP initializationOptions, e.g. `tsserver.path` for a globally installed
  typescript ≤5.x; the TS 7 native preview has no `lib/tsserver.js` and won't
  work).
- `enable: true|false` per language (omitted = on).
- The default theme is a tuned gruvbox dark; any chroma style name works,
  rendered truecolor and auto-degraded on 256-color terminals.
- A project's `.ntee-editor.yaml` can never name an executable: anything that
  could run a program is stripped from it, so cloning a repo never hands it
  code execution.

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
| `auto_drive` | ``ctrl+` `` | review: auto-drive on/off (also Ctrl+Space, what older terminals send for it) |

**By hand**: list only what you change; everything else keeps its default.
For example, goto on `Ctrl+N`, save on `F5`, and undo/redo swapped:

```yaml
keybinds:
  goto_file: ctrl+n
  save: f5
  undo: ctrl+y
  redo: ctrl+z
```

**What the keybinds pane writes**: the same bindings, saved as the full list
(alphabetical, every action spelled out) so the file always says exactly which
key does what:

```yaml
keybinds:
    auto_drive: ctrl+`
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
snapshots, drafts, tabs, the session, and agent edit sessions. ntee-db is
single-writer (flock): opening the same project twice falls back to in-memory
state with a notice. Undo still works, but nothing persists.

Old undo versions auto-evict per file, but dead space accumulates in the log
over time. Press `Ctrl+T` to see exactly how much, and `db compact` /
`db relieve` to reclaim it. No external tools needed.

## Architecture

Every `internal/` package has its own design doc under [doc/](doc/). Start
with [doc/README.md](doc/README.md) for the package map and how the pieces fit:
the Bubble Tea model, the never-block-the-UI rule, the file jail, the LSP
manager, the agent staging layers, and the MCP server.

## License

Apache License 2.0, see [LICENSE](LICENSE). Copyright 2026 Nick An.

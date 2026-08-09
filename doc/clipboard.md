# internal/clipboard

**Introduction**

The smallest package of the three: it copies text to the system clipboard on macOS and Linux, and that's it. The editor calls `Copy` for yank-to-system-clipboard; there is no paste side.

The design constraint is coverage across environments. Native clipboard CLIs (`pbcopy`, `wl-copy`, `xclip`, `xsel`) are the most reliable path locally — including in terminals like Apple Terminal that don't support OSC 52 — so they're tried first. When no tool is available, as in SSH, tmux, or headless sessions, the package falls back to writing an OSC 52 escape sequence and lets the user's terminal emulator set the clipboard on its end.

**Architecture**

One file, one exported function. `clipCmd` names a candidate CLI plus its arguments; `nativeCommands` returns the ordered candidate list for the current OS (`pbcopy` on darwin; `wl-copy` for Wayland, then `xclip`, then `xsel` for X11 on linux). `Copy` walks that list and falls back to the OSC 52 path, which is split into building the escape sequence and writing it to the terminal.

**Functions**

### clipboard.go

- `Copy` — the package's public surface. Tries each native command in order: look up the binary, pipe the text to its stdin, and stop on the first success. A tool that is present but fails doesn't abort — the next candidate gets a shot, and OSC 52 is the final fallback.
- `writeOSC52` — emits the escape sequence to `/dev/tty` rather than stdout, so it reaches the terminal out-of-band without disturbing the editor's alt-screen render.

*Plus small helpers: `nativeCommands` — the per-OS candidate list, and `osc52Seq` — builds the `ESC ] 52 ; c ;` + base64 clipboard-set sequence.*

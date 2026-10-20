# internal/keymap

**Introduction**

This package decides which key chord triggers which editor action. Before it existed every chord was a hardcoded string compare (`k == "ctrl+p"`); now the app asks a `Keymap` instead, so a user can move goto from Ctrl+P to Ctrl+N or save to F5. The effective map is the built-in defaults overlaid with the user config's `keybinds:` section, edited from the Ctrl+T keybinds pane or by hand. Keeping it in `config.yaml` (not ntee-db) is deliberate: the file is easy to copy to another machine.

The package is pure — no I/O, no Bubble Tea types. Keys are the strings Bubble Tea's `KeyPressMsg.String()` produces (`"ctrl+n"`, `"alt+x"`, `"shift+tab"`, `"f5"`), so matching is a string compare.

**Architecture**

`Action` is an enum of the rebindable functions. `actionSpecs`, indexed by `Action`, gives each its config name (snake_case, matching the rest of the config), default key, and a short description; its order is the display order of the keybinds pane.

| Action | Default | | Action | Default |
|---|---|---|---|---|
| `quit` | ctrl+q | | `save` | ctrl+s |
| `repo_picker` | ctrl+w | | `undo` | ctrl+z |
| `goto_file` | ctrl+p | | `redo` | ctrl+y |
| `uncommitted` | ctrl+u | | `find` | ctrl+f |
| `grep` | ctrl+g | | `jump_to_reference` | ctrl+j |
| `inspect` | ctrl+t | | `jump_back` | ctrl+o |
| `op_mode` | ctrl+r | | `expand_selection` | ctrl+a |
| `next_tab` | shift+tab | | `exec` | ctrl+e |

`Keymap` is a fixed-size array of keys indexed by `Action` — a small value type, so the app's by-value `Model` carries it without sharing state between copies, and lookups never allocate (they run per keystroke).

Three rules hold for every keymap this package hands out:

- **One namespace.** No two actions share a key. Global chords are checked before mode dispatch, so a global key would shadow an edit-mode one anyway; a single namespace makes that impossible to configure by accident.
- **Only chords are bindable.** A key must carry `ctrl+` or `alt+`, or be F1–F12 (optionally shifted), or `shift+tab`. Plain and Shift-only keys, Enter, Esc, Tab, Space and bare arrows would break typing or navigation. `ctrl+c` is reserved as the always-quit escape hatch, and `ctrl+i`/`ctrl+m`/`ctrl+[`/`ctrl+h` are refused because most terminals report them as Tab/Enter/Esc/Backspace, so the binding would never fire.
- **A bad entry never takes a key away.** `Resolve` keeps the default for any entry it rejects and reports why, so a typo in `config.yaml` degrades to a startup notice, not a lost shortcut.

**Functions**

### keymap.go

- `Resolve` — builds the effective keymap from the config map. It overlays the entries in sorted order, warning on unknown actions and unbindable keys. Then it removes duplicates: each pass reverts *every* override caught sharing a key (so neither side of a tie wins by map order) and repeats until none remain. Defaults never collide, so the override set shrinks every pass and the loop ends; a pure swap (undo ↔ redo) has no duplicate and survives.
- `Keymap.With` — the edit path used by the keybinds pane and the `keybind` bar command: a copy with one action rebound, or an error naming the action that already holds the key. Rebinding an action to its own key is a no-op, not a conflict.
- `Keymap.ActionFor` / `Keymap.Matches` — the per-keystroke lookups. `ActionFor` is a linear scan over 16 strings, cheaper than a map and allocation-free.
- `Keymap.Bindings` — the full name→key map. The app always writes this whole map to `config.yaml`, so the file lists every binding explicitly; legacy files with an empty or partial section still load, with defaults filling the gaps.
- `ValidKey` — the bindability rule above, with a readable reason on rejection.
- `Label` — display form for hints and the pane: `ctrl+shift+s` → `Ctrl+Shift+S`.

*Plus small helpers: `Default`, `Actions`, `ActionByName`, `Action.Name`/`DefaultKey`/`Description`, `Keymap.Key`/`Label`/`IsCustom`/`CustomCount`, `Normalize` (trim + lower-case a configured key), `isFunctionKey`, `boundTo` — lookups, defaults, and parsing.*

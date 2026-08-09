# internal/config

**Introduction**

This package loads the editor's YAML configuration and hands the rest of the app one effective `Config`. Three layers merge in order: built-in defaults, the user's `~/.config/ntee-editor/config.yaml`, then the project-local `<project>/.ntee-editor.yaml`. Later layers override fields they set, and most lists replace outright.

Two design rules dominate. First, the trust rule: only the *user* config may name executables. The project-local file ships with whatever repo the editor is pointed at, so its per-language `lsp` blocks (command, args, init, bridge — all execution vectors) and `install` strategies are stripped before merging. It may still set behavior: enable flags, extensions, editor, tree, and theme settings. Second, `extensions` union: a language's extensions are unioned with the defaults rather than replaced, so config can extend — never shrink — the set of file types routed to an LSP server.

The package also edits the user config on the app's behalf (adding languages, toggling enable flags, setting the theme). Those helpers are deliberately cautious: they refuse to edit on top of a malformed file, and they back up the prior content to `config.yaml.bak` before rewriting, since a rewrite loses the file's comments. And loading never fails silently — `LoadWithWarnings` reports malformed files instead of quietly behaving like defaults.

**Architecture**

`Config` is the root type: `EditorConfig` (tab width, snapshot cap, highlight size cap), `TreeConfig` (ignore list, search-corpus cap), `ThemeConfig`, a `Languages` map of `LanguageConfig` (enable flag, extensions, optional `LSPServerConfig`, `InstallStrategy` list for `--prepare-lsp`), and the global `LSPConfig` switch. `LSPServerConfig` can carry a `BridgeConfig` wiring a hybrid server (like Volar) to a companion server's `executeCommand`. `Keybinds` is reserved and not applied in v1.

The merge flow: `LoadWithWarnings` starts from `Default()`, then calls `merge` twice — user config with `trustExec: true`, project config with `trustExec: false`. Inside `merge`, languages are decoded separately from the rest (plain unmarshal would replace, and extensions need union), the trust gate strips `LSP`/`Install` from untrusted files, and `mergeLanguages` folds the result back in. Finally, a few numeric fields are clamped to sane defaults.

Everything lives in one file, `config.go`.

**Functions**

### config.go

- `Default` — the built-in configuration: tab width 4, 50 snapshots, a curated `tree.ignore` list of build/dependency dirs (on top of the always-hidden `.git` and always-dimmed `node_modules`), gruvbox syntax theme, and LSP recipes for Go (`gopls`) and TypeScript (`typescript-language-server`, which also covers JavaScript). LSP defaults to enabled — safe because a missing server binary degrades to a one-time notice.
- `LoadWithWarnings` — builds the effective config for a project root and returns any warnings. Missing files are fine; a malformed one is skipped so the editor still starts, but the skip is reported — a typo'd config silently acting like defaults is worse than a notice. Also clamps `TabWidth`, `MaxSnapshots`, and `MaxIndexFiles` when set below 1.
- `Load` — `LoadWithWarnings` with the warnings discarded, for callers that don't surface them.
- `ConfigPath` — the user config location: `$XDG_CONFIG_HOME` (else `~/.config`) + `ntee-editor/config.yaml`.
- `merge` — reads one YAML file into the accumulating config. Languages are pulled out and merged with union semantics; when `trustExec` is false, every language's `LSP` and `Install` blocks are nil'd before merging — that's the whole trust gate. Parse errors warn as "partially applied" because fields decoded before the error may already have landed.
- `mergeLanguages` — overlays a file's languages onto the accumulated map: extensions union, `LSP`/`Enabled`/`Install` overlay only when set, new languages are added whole.
- `mergeLSP` — field-by-field overlay of one server config onto another; a nil base is replaced whole.
- `readUserConfig` — loads the user config for *editing*. If the file is malformed it refuses to proceed, because continuing would overwrite the user's recoverable file with a defaults-seeded one. If the file is absent, non-language sections are seeded from defaults — otherwise marshalling would emit zero values like `lsp.enabled: false`, which would disable all LSP on the next load. Languages stay empty so only explicit entries get written.
- `writeUserConfig` — marshals and writes the config, backing the prior content up to `path+".bak"` first. No backup means no rewrite: the rewrite loses comments and the backup is the only way back.
- `MergeUserLanguages` — adds language entries to the user config, skipping any already present so the user's tuning is never clobbered. Returns the (sorted) names actually added; writes nothing if that list is empty.
- `SetLanguagesEnabled` — writes `enable:` flags for named languages into the user config (the special name `"all"` toggles global `lsp.enabled` instead). Unlike `MergeUserLanguages` it does modify existing entries — but only the enable flag.
- `SetThemeSyntax` — writes `theme.syntax` into the user config, creating it if needed.
- `LanguageConfig.IsEnabled` — a language is enabled when the flag is unset or true; only an explicit `enable: false` turns it off.

*Plus small helpers: `firstLine` — clips a multi-line YAML error to its first line for warnings, and `unionStrings` — order-preserving, de-duped list union backing the extensions merge.*

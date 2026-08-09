# internal/lspsetup

**Introduction**

This package is the engine behind `ntee-editor --prepare-lsp`: a small, curated package-manager-of-recipes that gets language servers installed and configured without the user hand-editing YAML. It is used only by the CLI flag — the editor itself never calls it at runtime.

The design constraints that shape it:

- **Recipes are curated and pinned.** Each supported language (go, typescript, java, kotlin, ruby, python, vue) has a built-in recipe naming exactly how to install its server via the platform's native tool — brew, `go install`, npm, or gem. Versions are pinned deliberately: whatever gets installed is auto-executed by the editor on the next matching file open, so "latest" would mean running unreviewed code the moment upstream publishes it. Brew is the one exception, since `brew install` has no version syntax.
- **Installs can't hang forever.** Each installer invocation runs under a 10-minute timeout — generous, because a cold brew formula with dependencies genuinely takes minutes, but a wedged package manager no longer stalls the flow. (Same philosophy as `gitcmd.Timeout`: bound against the worst *legitimate* case.)
- **The user's global TypeScript is never touched.** The TypeScript language server (and Vue's hybrid mode) needs a *classic-line* TypeScript with `typescript.js`; the 7.x native preview doesn't have one. Rather than clobber a global `typescript` the user may want to keep, a pinned `typescript@5` is installed into a private editor toolchain at `~/.ntee-editor/toolchain` and the server is pointed at it via `tsserver.path`.
- **Generated config uses resolved absolute paths.** After a successful install the server binary is re-verified with the same resolution rules the editor uses (`lsp.ResolveBinary`), and the *resolved* path — usually absolute — is what gets written to the user config, so the editor finds it regardless of the launching shell's PATH. Recipes only ever land in the *user* config: per the config trust rule (internal/config), project-local configs may not name executables at all.
- **Nothing happens without confirmation, and user edits survive.** The flow is plan → print → confirm → install → merge; `config.MergeUserLanguages` preserves entries the user already tuned.

**Architecture**

The whole package is one file, `lspsetup.go`, built around two pieces:

- **`Recipes()`** — the static registry: a `map[string]config.LanguageConfig` where each entry carries the server invocation (the `LSP` block) and one or more `Install` strategies (kind, package/formula with pinned version, required runtime, platform filter). Vue's recipe declares the hybrid bridge to typescript; the `--tsdk` arg and the `@vue/typescript-plugin` injection are *not* in the recipe — they're wired at prepare time from resolved paths.
- **`Preparer`** — the flow driver. Everything that touches the outside world is an injected function field: `LookPath` (tool/runtime presence), `Verify` (server resolution, defaulting to `lsp.ResolveBinary` so installs are checked exactly as the editor will check them), `Run` (the timeout-wrapped installer exec), and `TSDK`/`Plugin` (classic-TS and Vue-plugin resolution). That makes strategy selection and hybrid wiring testable without shelling out or a real filesystem.

Data flow: `Prepare` builds a `Preparer` (optionally filtering recipes to named languages) → `Plan` classifies each language as installed / ready / skipped → `Execute` prints the plan, asks once, runs the installs, verifies each result, generates configs from resolved paths → `wireHybrid` completes or degrades Vue hybrid mode → `config.MergeUserLanguages` writes the user config. There's no concurrency — installs run sequentially with output streamed to the caller's writer.

**Functions**

### lspsetup.go

- `Recipes` — the built-in per-language install recipes, with pinned versions (gopls v0.23.0, typescript-language-server 5.3.0, pyright 1.1.411, ruby-lsp 0.26.10, @vue/language-server 3.3.9). The doc comment doubles as the pin-bump procedure: check upstream releases, update the string (and `ensureClassicTS` for the TS pin), and run `--prepare-lsp` to verify install + handshake end to end.
- `NewPreparer` — wires a `Preparer` to the real platform: `exec.LookPath`, `lsp.ResolveBinary`, and a `Run` that executes under the 10-minute `installTimeout` with a 5-second `WaitDelay` so pipes are reclaimed even if a grandchild inherits them, converting a context kill into a readable "timed out" error.
- `Prepare` — the CLI entry point: build the Preparer, optionally filter to the named languages (unknown names error with the valid list), and run `Execute`.
- `Plan` — classifies every recipe language without changing anything: disabled → skipped; server already resolvable (and, for a bridge target, tsdk present) → installed; a viable strategy exists → ready; otherwise skipped with a reason naming what's missing.
- `choose` — picks the first install strategy whose platform matches, whose tool (brew/npm/…) exists, and whose required runtime (node/java/…) is on PATH; otherwise returns a "needs X or Y" reason built from the missing tools.
- `isInstalled` — "needs no install" is normally just "the server binary resolves", but a hybrid *companion* (typescript, which Vue bridges to) also needs a compatible classic TypeScript — the server alone isn't enough. A missing tsdk (e.g. only the 7.x native preview is present) flags the language as needing an install so the pinned `typescript@5` gets pulled in.
- `Execute` — the interactive flow: print the plan, confirm once if anything needs installing, run each install, re-verify the binary (an install that succeeds but isn't on PATH is called out rather than silently configured), generate configs from resolved paths, wire hybrid mode, and merge into the user config — reporting exactly which languages were added or that the config already covered them.
- `genConfig` — builds the `LanguageConfig` written to disk: the resolved command path, `enable: true` so the user can toggle later, and the recipe's args/init/bridge carried through.
- `wireHybrid` — completes Vue hybrid mode on the generated config: resolves a classic TS (installing the private one if needed), points typescript-language-server at it via `tsserver.path`, injects `--tsdk=<lib>` into the vue server's args and `@vue/typescript-plugin` into the typescript server's init. If any piece can't be resolved — no classic TS, plugin missing, typescript not being configured — it drops the bridge so Vue degrades to template-only features rather than crashing, and says so.
- `ensureClassicTS` — returns a resolvable classic-TS lib dir, installing `typescript@5.9.3` into the private toolchain (`npm install --prefix ~/.ntee-editor/toolchain`) if none is found. Never installs globally.
- `defaultResolveTSDK` — finds a classic-line TypeScript lib dir: the private toolchain first, then the npm global root in case a classic TS is already there. The check is the presence of `typescript.js`, which the 7.0.2 native preview lacks — so it's naturally rejected.
- `defaultResolvePlugin` — locates `@vue/typescript-plugin` bundled next to the vue language server, resolving symlinks first (npm global bins are symlinks) and walking from the bin script up to the package's `node_modules`.
- `installCmd` — maps a strategy to the actual command line: `brew install <formula>`, `go install <pkg@ver>`, `npm install -g <pkgs>`, and for gem — which has no `name@version` syntax — splits a `pkg@1.2.3` pin into `gem install pkg -v 1.2.3`.
- `filterRecipes` — case-insensitive subset selection for `--prepare-lsp <langs>`; an unknown name errors with the sorted list of valid languages.

*Plus small helpers: `toolchainDir`, `hasClassicTS`, `isBridgeTarget`, `resolves`, `recipeNames`, `toolFor`, `contains`, `appendUniq` — path/lookup one-liners and tiny predicates.*

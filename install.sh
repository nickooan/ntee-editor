#!/usr/bin/env bash
# ntee-editor bootstrap: checks Go (installing via Homebrew if missing),
# clones/builds the editor, and prepares language servers for every supported
# language whose runtime is present.
#
#   ./install.sh                       # from inside a checkout: build in place
#   curl -fsSL .../install.sh | bash   # standalone: clones to ~/.ntee-editor/src
#
# NTEE_INSTALL_DIR overrides the clone destination.
set -euo pipefail

REPO_URL="https://github.com/nickooan/ntee-editor"
MODULE="module github.com/nickooan/ntee-editor"
INSTALL_DIR="${NTEE_INSTALL_DIR:-$HOME/.ntee-editor/src}"
BIN_DIR="$HOME/go/bin"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- platform gate: macOS only for now -------------------------------------
[[ "$(uname -s)" == "Darwin" ]] || die "only macOS is supported for now (Linux support not implemented yet)"

# --- Go toolchain -----------------------------------------------------------
if command -v go >/dev/null 2>&1; then
    info "Go found: $(go version)"
else
    command -v brew >/dev/null 2>&1 || die "Go is not installed and Homebrew is missing — install Homebrew first: https://brew.sh"
    info "Go not found — installing via Homebrew"
    brew install go
    command -v go >/dev/null 2>&1 || die "Go installation failed (go still not on PATH)"
fi

# --- source: in-place checkout or clone -------------------------------------
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" >/dev/null 2>&1 && pwd)"
if [[ -f "$script_dir/go.mod" ]] && head -1 "$script_dir/go.mod" | grep -q "^$MODULE$"; then
    src="$script_dir"
    info "building from existing checkout: $src"
elif [[ -d "$INSTALL_DIR/.git" ]]; then
    info "updating existing clone: $INSTALL_DIR"
    git -C "$INSTALL_DIR" pull --ff-only
    src="$INSTALL_DIR"
else
    info "cloning $REPO_URL → $INSTALL_DIR"
    mkdir -p "$(dirname "$INSTALL_DIR")"
    git clone "$REPO_URL" "$INSTALL_DIR"
    src="$INSTALL_DIR"
fi

# --- build ------------------------------------------------------------------
info "building ntee → $BIN_DIR/ntee"
mkdir -p "$BIN_DIR"
(cd "$src" && go build -trimpath -o "$BIN_DIR/ntee" ./cmd/ntee)

case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*)
    info "note: $BIN_DIR is not on your PATH — add this to your shell profile:"
    printf '    export PATH="%s:$PATH"\n' "$BIN_DIR"
    ;;
esac

# --- language servers -------------------------------------------------------
if [[ "${NTEE_SKIP_LSP:-}" == "1" ]]; then
    info "skipping language server setup (NTEE_SKIP_LSP=1) — run later: ntee --prepare-lsp"
else
    info "preparing language servers (all supported languages with a runtime present)"
    "$BIN_DIR/ntee" --prepare-lsp --yes
fi

info "done"
echo "  binary:  $BIN_DIR/ntee"
echo "  config:  ${XDG_CONFIG_HOME:-$HOME/.config}/ntee-editor/config.yaml"
echo "  run:     ntee <project-dir>"

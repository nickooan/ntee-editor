#!/usr/bin/env bash
# ntee-editor bootstrap: checks Go (installing via the platform's package
# manager if missing), clones/builds the editor, and prepares language servers
# for every supported language whose runtime is present. Re-running updates an
# existing install (git pull + rebuild).
#
#   ./install.sh                       # from inside a checkout: build in place
#   curl -fsSL .../install.sh | bash   # standalone: clones to ~/.ntee-editor/src
#
# NTEE_INSTALL_DIR overrides the clone destination; NTEE_SKIP_LSP=1 skips the
# language-server setup step.
set -euo pipefail

REPO_URL="https://github.com/nickooan/ntee-editor"
MODULE="module github.com/nickooan/ntee-editor"
INSTALL_DIR="${NTEE_INSTALL_DIR:-$HOME/.ntee-editor/src}"
BIN_DIR="$HOME/go/bin"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- platform gate ----------------------------------------------------------
OS="$(uname -s)"
case "$OS" in
Darwin | Linux) ;;
*) die "unsupported platform: $OS (only macOS and Linux are supported)" ;;
esac

# --- prerequisites ----------------------------------------------------------
command -v git >/dev/null 2>&1 || die "git is required — install it first (macOS: xcode-select --install · Debian/Ubuntu: sudo apt-get install git)"

install_go() {
    if [[ "$OS" == "Darwin" ]]; then
        command -v brew >/dev/null 2>&1 || die "Go is not installed and Homebrew is missing — install Homebrew first: https://brew.sh"
        info "Go not found — installing via Homebrew"
        brew install go
        return
    fi
    # Linux: prefer brew when present, else the distro package manager.
    if command -v brew >/dev/null 2>&1; then
        info "Go not found — installing via Homebrew"
        brew install go
    elif command -v apt-get >/dev/null 2>&1; then
        info "Go not found — installing via apt-get (sudo)"
        sudo apt-get update && sudo apt-get install -y golang-go
    elif command -v dnf >/dev/null 2>&1; then
        info "Go not found — installing via dnf (sudo)"
        sudo dnf install -y golang
    elif command -v pacman >/dev/null 2>&1; then
        info "Go not found — installing via pacman (sudo)"
        sudo pacman -S --noconfirm go
    else
        die "Go is not installed and no supported package manager was found — install Go from https://go.dev/dl/ and re-run"
    fi
}

if command -v go >/dev/null 2>&1; then
    info "Go found: $(go version)"
else
    install_go
    command -v go >/dev/null 2>&1 || die "Go installation failed (go still not on PATH) — install from https://go.dev/dl/ and re-run"
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

# PATH status is reported in the final summary (path_hint) so the
# instructions are the last thing the user sees.
on_path=true
case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) on_path=false ;;
esac

# --- language servers -------------------------------------------------------
if [[ "${NTEE_SKIP_LSP:-}" == "1" ]]; then
    info "skipping language server setup (NTEE_SKIP_LSP=1) — run later: ntee --prepare-lsp"
else
    info "preparing language servers (all supported languages with a runtime present)"
    "$BIN_DIR/ntee" --prepare-lsp --yes
fi

# path_hint prints copy-paste instructions for putting the binary on PATH,
# tailored to the user's login shell.
path_hint() {
    local profile
    case "$(basename "${SHELL:-}")" in
    zsh) profile="$HOME/.zshrc" ;;
    bash)
        if [[ "$OS" == "Darwin" ]]; then profile="$HOME/.bash_profile"; else profile="$HOME/.bashrc"; fi
        ;;
    *)
        echo "  ntee is not on your PATH — add this line to your shell profile:"
        printf '      export PATH="%s:$PATH"\n' "$BIN_DIR"
        return
        ;;
    esac
    echo "  ntee is not on your PATH yet. To use it globally, run:"
    printf '      echo '\''export PATH="$HOME/go/bin:$PATH"'\'' >> %s && source %s\n' "$profile" "$profile"
}

info "done"
echo "  binary:  $BIN_DIR/ntee"
echo "  config:  ${XDG_CONFIG_HOME:-$HOME/.config}/ntee-editor/config.yaml"
echo "  run:     ntee <project-dir>"
if ! $on_path; then
    echo
    path_hint
fi

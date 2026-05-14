#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_ROOT="${NEWAPI_ROOT:-$(cd -- "$SCRIPT_DIR/../../../.." && pwd)}"
BUN_BIN="${BUN_BIN:-}"
TARGET="all"

cd "$APP_ROOT"

log() {
  printf '[newapi-frontend-build] %s\n' "$*"
}

fail() {
  printf '[newapi-frontend-build] ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: build-frontend.sh [all|default|classic]

Builds frontend dist assets required by Go //go:embed:
  web/default/dist
  web/classic/dist

Options:
  -h, --help      Show this help.

Environment overrides:
  BUN_BIN=/path/to/bun
  NEWAPI_BUN_INSTALL=0
  NEWAPI_BUN_RETRY_CLEAR_CACHE=0
  NEWAPI_VERSION=<version>
EOF
}

parse_args() {
  while (($# > 0)); do
    case "$1" in
      all|default|classic)
        TARGET="$1"
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        fail "unknown argument: $1"
        ;;
    esac
  done
}

resolve_bun() {
  if [[ -n "$BUN_BIN" ]]; then
    [[ -x "$BUN_BIN" ]] || fail "BUN_BIN is not executable: $BUN_BIN"
    return
  fi

  if command -v bun >/dev/null 2>&1; then
    BUN_BIN="$(command -v bun)"
    return
  fi

  if [[ -x "$HOME/.bun/bin/bun" ]]; then
    BUN_BIN="$HOME/.bun/bin/bun"
    return
  fi

  fail "required command not found: bun. Install Bun or set BUN_BIN=/path/to/bun"
}

resolve_version() {
  if [[ -n "${NEWAPI_VERSION:-}" ]]; then
    printf '%s\n' "$NEWAPI_VERSION"
    return
  fi

  if [[ -s VERSION ]]; then
    tr -d '\n\r' < VERSION
    return
  fi

  if command -v git >/dev/null 2>&1; then
    git describe --tags --always --dirty 2>/dev/null || printf 'dev'
    return
  fi

  printf 'dev'
}

maybe_bun_install() {
  local dir="$1"
  [[ "${NEWAPI_BUN_INSTALL:-1}" == "0" ]] && return

  local args=(install)
  if [[ -f "$dir/bun.lock" ]]; then
    args+=(--frozen-lockfile)
  fi

  if (cd "$dir" && "$BUN_BIN" "${args[@]}"); then
    return
  fi

  [[ "${NEWAPI_BUN_RETRY_CLEAR_CACHE:-1}" == "0" ]] && return 1

  log "bun install failed; clearing Bun cache and retrying once"
  "$BUN_BIN" pm cache rm >/dev/null 2>&1 || true
  (cd "$dir" && "$BUN_BIN" "${args[@]}")
}

build_default() {
  log "building default frontend"
  maybe_bun_install "$APP_ROOT/web/default"
  (
    cd "$APP_ROOT/web/default"
    DISABLE_ESLINT_PLUGIN=true VITE_REACT_APP_VERSION="$VERSION" "$BUN_BIN" run build
  )
}

build_classic() {
  log "building classic frontend"
  maybe_bun_install "$APP_ROOT/web/classic"
  (
    cd "$APP_ROOT/web/classic"
    VITE_REACT_APP_VERSION="$VERSION" "$BUN_BIN" run build
  )
}

main() {
  parse_args "$@"
  resolve_bun
  VERSION="$(resolve_version)"
  export VERSION

  log "version: $VERSION"
  case "$TARGET" in
    all)
      build_default
      build_classic
      ;;
    default)
      build_default
      ;;
    classic)
      build_classic
      ;;
  esac

  log "frontend build complete"
}

main "$@"

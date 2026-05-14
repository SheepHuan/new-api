#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_ROOT="${NEWAPI_ROOT:-$(cd -- "$SCRIPT_DIR/../../../.." && pwd)}"
APP_NAME="new-api"

cd "$APP_ROOT"

DEPLOY_DIR="${NEWAPI_DEPLOY_DIR:-$APP_ROOT/.codex-deploy}"
BIN_DIR="$DEPLOY_DIR/bin"
RUN_DIR="$DEPLOY_DIR/run"
LOG_DIR="$DEPLOY_DIR/logs"
BIN_PATH="$BIN_DIR/$APP_NAME"
PID_FILE="${NEWAPI_PID_FILE:-$RUN_DIR/$APP_NAME.pid}"
LOG_FILE="${NEWAPI_LOG_FILE:-$LOG_DIR/$APP_NAME.log}"
MOCK_PID_FILE="${NEWAPI_QUEUE_TEST_MOCK_PID_FILE:-$RUN_DIR/mock-openai.pid}"
MOCK_LOG_FILE="${NEWAPI_QUEUE_TEST_MOCK_LOG_FILE:-$LOG_DIR/mock-openai.log}"
PORT="${NEWAPI_PORT:-${PORT:-3000}}"
HEALTH_TIMEOUT="${NEWAPI_HEALTH_TIMEOUT:-30}"
DEFAULT_SQLITE_PATH="$APP_ROOT/data/new-api.sqlite?_busy_timeout=30000"
SQLITE_PATH_VALUE="${NEWAPI_SQLITE_PATH:-$DEFAULT_SQLITE_PATH}"
BUN_BIN="${BUN_BIN:-}"

log() {
  printf '[newapi-autodeploy] %s\n' "$*"
}

fail() {
  printf '[newapi-autodeploy] ERROR: %s\n' "$*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

usage() {
  cat <<'EOF'
Usage: deploy-newapi.sh [--port PORT]

Options:
  -p, --port PORT   Listen port. Defaults to 3000.
  -h, --help        Show this help.

Environment overrides:
  NEWAPI_PORT, NEWAPI_SQLITE_PATH, NEWAPI_SESSION_SECRET,
  NEWAPI_SKIP_FRONTEND_BUILD=1, NEWAPI_BUN_INSTALL=0,
  NEWAPI_BUN_RETRY_CLEAR_CACHE=0, NEWAPI_AUTO_SETUP=0,
  NEWAPI_ADMIN_USERNAME, NEWAPI_ADMIN_PASSWORD, NEWAPI_FRONTEND_THEME,
  NEWAPI_QUEUE_TEST_SEED=0, NEWAPI_QUEUE_TEST_PORTS,
  NEWAPI_QUEUE_TEST_DEFAULT_CHANNEL_RPM, NEWAPI_QUEUE_TEST_CHANNEL_RPM,
  NEWAPI_QUEUE_TEST_SCHEDULE_STRATEGY,
  NEWAPI_QUEUE_TEST_MOCK_SERVERS=0, NEWAPI_QUEUE_TEST_MOCK_TPS,
  NEWAPI_QUEUE_TEST_RELAX_RATE_LIMITS=0, BUN_BIN
EOF
}

parse_args() {
  while (($# > 0)); do
    case "$1" in
      -p|--port)
        [[ $# -ge 2 ]] || fail "$1 requires a port"
        PORT="$2"
        shift 2
        ;;
      --port=*)
        PORT="${1#*=}"
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      [0-9]*)
        PORT="$1"
        shift
        ;;
      *)
        fail "unknown argument: $1"
        ;;
    esac
  done

  [[ "$PORT" =~ ^[0-9]+$ ]] || fail "port must be numeric: $PORT"
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

sqlite_file_from_dsn() {
  local dsn="$1"
  dsn="${dsn%%\?*}"
  printf '%s\n' "$dsn"
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

ensure_runtime_dirs() {
  mkdir -p "$BIN_DIR" "$RUN_DIR" "$LOG_DIR"

  local sqlite_file
  sqlite_file="$(sqlite_file_from_dsn "$SQLITE_PATH_VALUE")"
  if [[ "$sqlite_file" != ":memory:" ]]; then
    mkdir -p "$(dirname -- "$sqlite_file")"
  fi
}

ensure_session_secret() {
  if [[ -n "${SESSION_SECRET:-}" && "${SESSION_SECRET}" != "random_string" ]]; then
    return
  fi

  if [[ -n "${NEWAPI_SESSION_SECRET:-}" ]]; then
    export SESSION_SECRET="$NEWAPI_SESSION_SECRET"
    return
  fi

  local secret_file="${NEWAPI_SESSION_SECRET_FILE:-$RUN_DIR/session-secret}"
  if [[ ! -s "$secret_file" ]]; then
    umask 077
    if command -v openssl >/dev/null 2>&1; then
      openssl rand -hex 32 > "$secret_file"
    else
      printf '%s-%s-%s\n' "$(date +%s)" "$$" "$RANDOM" > "$secret_file"
    fi
  fi
  export SESSION_SECRET
  SESSION_SECRET="$(tr -d '\n\r' < "$secret_file")"
}

configure_database_env() {
  if [[ -n "${NEWAPI_SQL_DSN:-}" ]]; then
    export SQL_DSN="$NEWAPI_SQL_DSN"
    if [[ -n "${NEWAPI_LOG_SQL_DSN:-}" ]]; then
      export LOG_SQL_DSN="$NEWAPI_LOG_SQL_DSN"
    else
      unset LOG_SQL_DSN
    fi
    return
  fi

  unset SQL_DSN
  unset LOG_SQL_DSN
  export SQLITE_PATH="$SQLITE_PATH_VALUE"
}

configure_queue_seed_rate_limits() {
  [[ "${NEWAPI_QUEUE_TEST_SEED:-1}" == "0" ]] && return
  [[ "${NEWAPI_QUEUE_TEST_RELAX_RATE_LIMITS:-1}" == "0" ]] && return

  export GLOBAL_API_RATE_LIMIT_ENABLE="${GLOBAL_API_RATE_LIMIT_ENABLE:-false}"
  export CRITICAL_RATE_LIMIT_ENABLE="${CRITICAL_RATE_LIMIT_ENABLE:-false}"
  export SEARCH_RATE_LIMIT_ENABLE="${SEARCH_RATE_LIMIT_ENABLE:-false}"
}

collect_old_pids() {
  local -n out_ref="$1"
  local pid
  declare -A seen=()

  if [[ -f "$PID_FILE" ]]; then
    pid="$(tr -dc '0-9' < "$PID_FILE" || true)"
    if [[ -n "$pid" && "$pid" != "$$" ]] && kill -0 "$pid" >/dev/null 2>&1; then
      seen["$pid"]=1
    fi
  fi

  while IFS= read -r pid; do
    if [[ -n "$pid" && "$pid" != "$$" ]] && kill -0 "$pid" >/dev/null 2>&1; then
      seen["$pid"]=1
    fi
  done < <(pgrep -x "$APP_NAME" 2>/dev/null || true)

  out_ref=()
  for pid in "${!seen[@]}"; do
    out_ref+=("$pid")
  done
}

collect_port_pids() {
  local port="$1"
  local -n out_ref="$2"
  local line pid
  declare -A seen=()

  if command -v lsof >/dev/null 2>&1; then
    while IFS= read -r pid; do
      [[ -n "$pid" && "$pid" != "$$" ]] && seen["$pid"]=1
    done < <(lsof -nP -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
  fi

  if command -v ss >/dev/null 2>&1; then
    while IFS= read -r line; do
      while [[ "$line" =~ pid=([0-9]+) ]]; do
        pid="${BASH_REMATCH[1]}"
        [[ "$pid" != "$$" ]] && seen["$pid"]=1
        line="${line#*pid=$pid}"
      done
    done < <(ss -ltnp "sport = :$port" 2>/dev/null || true)
  fi

  if command -v fuser >/dev/null 2>&1; then
    while IFS= read -r pid; do
      [[ -n "$pid" && "$pid" != "$$" ]] && seen["$pid"]=1
    done < <(fuser -n tcp "$port" 2>/dev/null | tr ' ' '\n' || true)
  fi

  out_ref=()
  for pid in "${!seen[@]}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      out_ref+=("$pid")
    fi
  done
}

parse_queue_test_ports() {
  local raw="${NEWAPI_QUEUE_TEST_PORTS:-12000,12001,12003}"
  raw="${raw//,/ }"
  local ports=()
  local port
  for port in $raw; do
    [[ "$port" =~ ^[0-9]+$ ]] || fail "queue test port must be numeric: $port"
    ((port > 0 && port <= 65535)) || fail "queue test port out of range: $port"
    [[ "$port" != "$PORT" ]] || fail "queue test mock port cannot match app port: $port"
    ports+=("$port")
  done
  ((${#ports[@]} > 0)) || fail "NEWAPI_QUEUE_TEST_PORTS must contain at least one port"
  printf '%s\n' "${ports[@]}"
}

terminate_pids() {
  local reason="$1"
  shift
  local pids=("$@")

  if ((${#pids[@]} == 0)); then
    return
  fi

  log "stopping $reason: ${pids[*]}"
  kill -TERM "${pids[@]}" 2>/dev/null || true

  local alive=()
  for _ in {1..40}; do
    alive=()
    for pid in "${pids[@]}"; do
      if kill -0 "$pid" >/dev/null 2>&1; then
        alive+=("$pid")
      fi
    done
    ((${#alive[@]} == 0)) && break
    sleep 0.25
  done

  if ((${#alive[@]} > 0)); then
    log "force killing stubborn process(es): ${alive[*]}"
    kill -KILL "${alive[@]}" 2>/dev/null || true
  fi

  rm -f "$PID_FILE"
}

terminate_mock_pids() {
  local pids=("$@")

  if ((${#pids[@]} == 0)); then
    return
  fi

  log "stopping queue mock upstream process(es): ${pids[*]}"
  kill -TERM "${pids[@]}" 2>/dev/null || true

  local alive=()
  for _ in {1..40}; do
    alive=()
    local pid
    for pid in "${pids[@]}"; do
      if kill -0 "$pid" >/dev/null 2>&1; then
        alive+=("$pid")
      fi
    done
    ((${#alive[@]} == 0)) && break
    sleep 0.25
  done

  if ((${#alive[@]} > 0)); then
    log "force killing stubborn queue mock process(es): ${alive[*]}"
    kill -KILL "${alive[@]}" 2>/dev/null || true
  fi

  rm -f "$MOCK_PID_FILE"
}

confirm_kill_port_processes() {
  local pids=()
  collect_port_pids "$PORT" pids

  if ((${#pids[@]} == 0)); then
    log "port $PORT is free"
    return
  fi

  log "port $PORT is already used by PID(s): ${pids[*]}"
  if [[ ! -t 0 ]]; then
    fail "port $PORT is in use and no interactive terminal is available"
  fi

  local answer
  read -r -p "Kill process(es) on port $PORT and continue? [y/N] " answer
  case "$answer" in
    y|Y|yes|YES)
      terminate_pids "process(es) on port $PORT" "${pids[@]}"
      ;;
    *)
      fail "aborted because port $PORT is in use"
      ;;
  esac
}

stop_existing_processes() {
  local pids=()
  collect_old_pids pids

  if ((${#pids[@]} == 0)); then
    log "no existing $APP_NAME process found"
    rm -f "$PID_FILE"
    return
  fi

  terminate_pids "existing $APP_NAME process(es)" "${pids[@]}"
}

maybe_start_queue_mock_servers() {
  local enabled="${NEWAPI_QUEUE_TEST_MOCK_SERVERS:-}"
  if [[ -z "$enabled" ]]; then
    if [[ "${NEWAPI_QUEUE_TEST_SEED:-1}" == "0" ]]; then
      enabled="0"
    else
      enabled="1"
    fi
  fi
  enabled="$(normalize_bool NEWAPI_QUEUE_TEST_MOCK_SERVERS "$enabled")"
  [[ "$enabled" == "true" ]] || {
    log "queue mock upstreams disabled"
    return
  }

  need_cmd python3

  local mock_script="$APP_ROOT/.agents/skills/newapi-queue-loadtest/scripts/mock_openai_servers.py"
  [[ -f "$mock_script" ]] || fail "queue mock upstream script not found: $mock_script"

  local ports=()
  mapfile -t ports < <(parse_queue_test_ports)

  local pids=()
  declare -A seen=()
  local pid port
  if [[ -f "$MOCK_PID_FILE" ]]; then
    pid="$(tr -dc '0-9' < "$MOCK_PID_FILE" || true)"
    if [[ -n "$pid" && "$pid" != "$$" ]] && kill -0 "$pid" >/dev/null 2>&1; then
      seen["$pid"]=1
    fi
  fi
  for port in "${ports[@]}"; do
    local port_pids=()
    collect_port_pids "$port" port_pids
    for pid in "${port_pids[@]}"; do
      [[ -n "$pid" && "$pid" != "$$" ]] && seen["$pid"]=1
    done
  done
  for pid in "${!seen[@]}"; do
    pids+=("$pid")
  done
  terminate_mock_pids "${pids[@]}"

  : > "$MOCK_LOG_FILE"
  log "starting queue mock upstreams on ports: ${ports[*]}"
  (
    cd "$APP_ROOT"
    nohup python3 "$mock_script" \
      --tokens-per-second "${NEWAPI_QUEUE_TEST_MOCK_TPS:-${NEWAPI_QUEUE_TEST_MOCK_TOKENS_PER_SECOND:-10}}" \
      --ok-count "${NEWAPI_QUEUE_TEST_MOCK_OK_COUNT:-100}" \
      --delay-ms "${NEWAPI_QUEUE_TEST_MOCK_DELAY_MS:-50}" \
      --jitter-ms "${NEWAPI_QUEUE_TEST_MOCK_JITTER_MS:-0}" \
      --failure-rate "${NEWAPI_QUEUE_TEST_MOCK_FAILURE_RATE:-0}" \
      --stats-interval "${NEWAPI_QUEUE_TEST_MOCK_STATS_INTERVAL:-5}" \
      "${ports[@]}" >> "$MOCK_LOG_FILE" 2>&1 &
    printf '%s\n' "$!" > "$MOCK_PID_FILE"
  )

  pid="$(cat "$MOCK_PID_FILE")"
  sleep 0.5
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    tail -n 80 "$MOCK_LOG_FILE" >&2 || true
    fail "queue mock upstream exited during startup"
  fi

  for port in "${ports[@]}"; do
    if command -v curl >/dev/null 2>&1; then
      curl --noproxy '*' -fsS --max-time 2 "http://127.0.0.1:$port/health" >/dev/null 2>&1 || fail "queue mock upstream health check failed on port $port"
    elif ! (echo > "/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1; then
      fail "queue mock upstream health check failed on port $port"
    fi
  done

  log "queue mock upstreams ready; pid: $pid"
  log "queue mock log: $MOCK_LOG_FILE"
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

build_frontend() {
  [[ "${NEWAPI_SKIP_FRONTEND_BUILD:-0}" == "1" ]] && {
    log "skipping frontend builds"
    return
  }

  resolve_bun

  log "building default frontend"
  maybe_bun_install "$APP_ROOT/web/default"
  (cd "$APP_ROOT/web/default" && DISABLE_ESLINT_PLUGIN=true VITE_REACT_APP_VERSION="$VERSION" "$BUN_BIN" run build)

  log "building classic frontend"
  maybe_bun_install "$APP_ROOT/web/classic"
  (cd "$APP_ROOT/web/classic" && VITE_REACT_APP_VERSION="$VERSION" "$BUN_BIN" run build)
}

build_binary() {
  need_cmd go

  log "building Go binary: $BIN_PATH"
  local ldflags="-s -w -X github.com/QuantumNous/new-api/common.Version=$VERSION"
  go build -ldflags "$ldflags" -o "$BIN_PATH" .
}

start_new_process() {
  export PORT
  export GIN_MODE="${GIN_MODE:-release}"
  export MEMORY_CACHE_ENABLED="${MEMORY_CACHE_ENABLED:-true}"

  log "starting $APP_NAME on port $PORT"
  : > "$LOG_FILE"
  (
    cd "$APP_ROOT"
    nohup "$BIN_PATH" --port "$PORT" --log-dir "$LOG_DIR" >> "$LOG_FILE" 2>&1 &
    printf '%s\n' "$!" > "$PID_FILE"
  )

  local pid
  pid="$(cat "$PID_FILE")"
  sleep 0.5
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    tail -n 80 "$LOG_FILE" >&2 || true
    fail "$APP_NAME exited during startup"
  fi
}

health_check() {
  local url="http://127.0.0.1:$PORT/api/status"
  local pid
  pid="$(cat "$PID_FILE")"

  log "waiting for health check: $url"
  for ((i = 0; i < HEALTH_TIMEOUT; i++)); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      tail -n 80 "$LOG_FILE" >&2 || true
      fail "$APP_NAME exited before becoming healthy"
    fi

    if command -v curl >/dev/null 2>&1; then
      if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
        log "deployment complete: http://127.0.0.1:$PORT"
        log "pid: $pid"
        log "log: $LOG_FILE"
        return
      fi
    elif (echo > "/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1; then
      log "deployment complete: http://127.0.0.1:$PORT"
      log "pid: $pid"
      log "log: $LOG_FILE"
      return
    fi

    sleep 1
  done

  tail -n 80 "$LOG_FILE" >&2 || true
  fail "health check timed out; inspect $LOG_FILE"
}

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\t'/\\t}"
  printf '%s' "$value"
}

normalize_bool() {
  local name="$1"
  local value="$2"
  case "$value" in
    true|false)
      printf '%s\n' "$value"
      ;;
    1|yes|YES|y|Y|on|ON)
      printf 'true\n'
      ;;
    0|no|NO|n|N|off|OFF|'')
      printf 'false\n'
      ;;
    *)
      fail "$name must be a boolean value: $value"
      ;;
  esac
}

auto_setup_admin() {
  [[ "${NEWAPI_AUTO_SETUP:-1}" == "0" ]] && {
    log "automatic setup disabled"
    return
  }

  if ! command -v curl >/dev/null 2>&1; then
    log "curl not found; skipping automatic admin setup"
    return
  fi

  local base_url="http://127.0.0.1:$PORT"
  local setup_response
  setup_response="$(curl --noproxy '*' -fsS --max-time 5 "$base_url/api/setup" 2>/dev/null || true)"
  if [[ "$setup_response" == *'"status":true'* ]]; then
    log "system already initialized; skipping automatic admin setup"
    return
  fi

  local username="${NEWAPI_ADMIN_USERNAME:-admin}"
  local password="${NEWAPI_ADMIN_PASSWORD:-admin123}"
  local self_use_mode demo_site
  self_use_mode="$(normalize_bool NEWAPI_SELF_USE_MODE_ENABLED "${NEWAPI_SELF_USE_MODE_ENABLED:-false}")"
  demo_site="$(normalize_bool NEWAPI_DEMO_SITE_ENABLED "${NEWAPI_DEMO_SITE_ENABLED:-false}")"

  [[ -n "$username" ]] || fail "NEWAPI_ADMIN_USERNAME cannot be empty"
  [[ -n "$password" ]] || fail "NEWAPI_ADMIN_PASSWORD cannot be empty"
  ((${#username} <= 12)) || fail "NEWAPI_ADMIN_USERNAME must be 12 characters or shorter"
  ((${#password} >= 8)) || fail "NEWAPI_ADMIN_PASSWORD must be at least 8 characters"

  local username_json password_json payload response
  username_json="$(json_escape "$username")"
  password_json="$(json_escape "$password")"
  payload="$(printf '{"username":"%s","password":"%s","confirmPassword":"%s","SelfUseModeEnabled":%s,"DemoSiteEnabled":%s}' \
    "$username_json" "$password_json" "$password_json" "$self_use_mode" "$demo_site")"

  response="$(
    curl --noproxy '*' -fsS --max-time 10 \
      -H 'Content-Type: application/json' \
      -X POST \
      --data "$payload" \
      "$base_url/api/setup"
  )"

  if [[ "$response" == *'"success":true'* ]]; then
    log "automatic admin setup complete: username=$username"
    return
  fi
  if [[ "$response" == *'系统已经初始化完成'* ]]; then
    log "system already initialized; skipping automatic admin setup"
    return
  fi

  fail "automatic admin setup failed: $response"
}

set_default_frontend_theme() {
  local theme="${NEWAPI_FRONTEND_THEME:-default}"
  case "$theme" in
    default|classic)
      ;;
    0|off|OFF|false|FALSE)
      log "frontend theme sync disabled"
      return
      ;;
    *)
      fail "NEWAPI_FRONTEND_THEME must be default or classic: $theme"
      ;;
  esac

  if ! command -v curl >/dev/null 2>&1; then
    log "curl not found; skipping frontend theme sync"
    return
  fi

  local base_url="http://127.0.0.1:$PORT"
  local username="${NEWAPI_ADMIN_USERNAME:-admin}"
  local password="${NEWAPI_ADMIN_PASSWORD:-admin123}"
  local cookie_file response username_json password_json payload theme_json

  cookie_file="$(mktemp)"
  trap 'rm -f "$cookie_file"' RETURN

  username_json="$(json_escape "$username")"
  password_json="$(json_escape "$password")"
  payload="$(printf '{"username":"%s","password":"%s"}' "$username_json" "$password_json")"

  response="$(
    curl --noproxy '*' -fsS --max-time 10 \
      -c "$cookie_file" \
      -H 'Content-Type: application/json' \
      -X POST \
      --data "$payload" \
      "$base_url/api/user/login" 2>/dev/null || true
  )"

  if [[ "$response" != *'"success":true'* ]]; then
    log "could not login as $username; skipping frontend theme sync"
    rm -f "$cookie_file"
    trap - RETURN
    return
  fi

  theme_json="$(json_escape "$theme")"
  payload="$(printf '{"key":"theme.frontend","value":"%s"}' "$theme_json")"
  response="$(
    curl --noproxy '*' -fsS --max-time 10 \
      -b "$cookie_file" \
      -H 'Content-Type: application/json' \
      -X PUT \
      --data "$payload" \
      "$base_url/api/option/" 2>/dev/null || true
  )"

  rm -f "$cookie_file"
  trap - RETURN

  if [[ "$response" == *'"success":true'* ]]; then
    log "frontend theme set to $theme"
    return
  fi

  log "frontend theme sync failed: ${response:-empty response}"
}

maybe_seed_queue_loadtest() {
  [[ "${NEWAPI_QUEUE_TEST_SEED:-1}" == "0" ]] && {
    log "queue load-test seed disabled"
    return
  }

  need_cmd python3

  local seed_script="$APP_ROOT/.agents/skills/newapi-queue-loadtest/scripts/seed_queue_test_data.py"
  [[ -f "$seed_script" ]] || fail "queue load-test seed script not found: $seed_script"

  local base_url="http://127.0.0.1:$PORT"
  local token_file="${NEWAPI_QUEUE_TEST_TOKENS_FILE:-$DEPLOY_DIR/queue-loadtest/tokens.json}"
  local seed_args=(
    --base-url "$base_url"
    --admin-username "${NEWAPI_ADMIN_USERNAME:-admin}"
    --admin-password "${NEWAPI_ADMIN_PASSWORD:-admin123}"
    --ports "${NEWAPI_QUEUE_TEST_PORTS:-12000,12001,12003}"
    --model "${NEWAPI_QUEUE_TEST_MODEL:-gpt-4o-mini}"
    --group "${NEWAPI_QUEUE_TEST_GROUP:-default}"
    --user-count "${NEWAPI_QUEUE_TEST_USER_COUNT:-64}"
    --user-password "${NEWAPI_QUEUE_TEST_USER_PASSWORD:-test123456}"
    --default-channel-rpm "${NEWAPI_QUEUE_TEST_DEFAULT_CHANNEL_RPM:-80}"
    --max-channel-pending "${NEWAPI_QUEUE_TEST_MAX_CHANNEL_PENDING:-512}"
    --default-user-max-pending "${NEWAPI_QUEUE_TEST_DEFAULT_USER_MAX_PENDING:-32}"
    --schedule-strategy "${NEWAPI_QUEUE_TEST_SCHEDULE_STRATEGY:-user_loop}"
    --tokens-file "$token_file"
    --sqlite-path "$SQLITE_PATH_VALUE"
    --token-mode "${NEWAPI_QUEUE_TEST_TOKEN_MODE:-auto}"
  )
  if [[ -n "${NEWAPI_QUEUE_TEST_CHANNEL_RPM:-}" ]]; then
    seed_args+=(--channel-rpm "$NEWAPI_QUEUE_TEST_CHANNEL_RPM")
  fi
  if [[ -n "${NEWAPI_QUEUE_TEST_USER_QUOTA:-}" ]]; then
    seed_args+=(--user-quota "$NEWAPI_QUEUE_TEST_USER_QUOTA")
  else
    seed_args+=(--user-quota-dollars "${NEWAPI_QUEUE_TEST_USER_QUOTA_DOLLARS:-10000000}")
  fi

  log "seeding queue load-test data"
  python3 "$seed_script" "${seed_args[@]}"
}

main() {
  parse_args "$@"
  ensure_runtime_dirs
  ensure_session_secret
  configure_database_env
  configure_queue_seed_rate_limits

  VERSION="$(resolve_version)"
  export VERSION
  log "version: $VERSION"
  log "database: ${SQL_DSN:+external SQL_DSN}${SQL_DSN:-SQLite at $SQLITE_PATH}"

  confirm_kill_port_processes
  stop_existing_processes
  build_frontend
  build_binary
  start_new_process
  health_check
  auto_setup_admin
  set_default_frontend_theme
  maybe_start_queue_mock_servers
  maybe_seed_queue_loadtest
}

main "$@"

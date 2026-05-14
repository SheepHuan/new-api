---
name: newapi-autodeploy
description: >-
  Build and redeploy this new-api repository locally. Use when asked to compile,
  rebuild, restart, deploy, auto-deploy new-api, or build only the frontend
  dist assets on the current machine, especially with the default SQLite backend
  and automatic shutdown of existing local new-api processes.
---

# new-api Local Auto-Deploy

Use the bundled deploy script for local compile-and-run workflows. It stops any
existing `new-api` process, rebuilds both frontends and the Go binary, then
starts the app with SQLite by default.

For frontend-only work, use `scripts/build-frontend.sh`. It builds the two
frontend dist directories required by Go `//go:embed` without compiling or
restarting the backend.

## Quick Start

From the repository root:

```bash
.agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
.agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh --port 3001
.agents/skills/newapi-autodeploy/scripts/build-frontend.sh
```

The script writes runtime files under `.codex-deploy/`:

- binary: `.codex-deploy/bin/new-api`
- PID file: `.codex-deploy/run/new-api.pid`
- mock upstream PID file: `.codex-deploy/run/mock-openai.pid`
- SQLite DB: `data/new-api.sqlite`
- app log: `.codex-deploy/logs/new-api.log`
- mock upstream log: `.codex-deploy/logs/mock-openai.log`
- stable session secret: `.codex-deploy/run/session-secret`

Default URL: `http://127.0.0.1:3000`

## Frontend Only

Build both frontend bundles:

```bash
.agents/skills/newapi-autodeploy/scripts/build-frontend.sh
```

Build only one frontend:

```bash
.agents/skills/newapi-autodeploy/scripts/build-frontend.sh default
.agents/skills/newapi-autodeploy/scripts/build-frontend.sh classic
```

Outputs:

- `web/default/dist`
- `web/classic/dist`

Use this when `go build` fails because `web/default/dist` or
`web/classic/dist` is missing.

## Options

Set environment variables before running the script:

```bash
NEWAPI_PORT=3001 .agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
NEWAPI_SQLITE_PATH="/data/new-api.sqlite?_busy_timeout=30000" .agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
NEWAPI_SKIP_FRONTEND_BUILD=1 .agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
NEWAPI_ADMIN_PASSWORD="change-me-now" .agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
NEWAPI_FRONTEND_THEME=classic .agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
NEWAPI_QUEUE_TEST_SEED=0 .agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh
```

Useful variables:

- `--port 3001`, `-p 3001`, or positional `3001`: command-line listen port override.
- `NEWAPI_PORT`: environment listen port override; defaults to `3000`.
- `NEWAPI_SQLITE_PATH`: SQLite DSN; defaults to `data/new-api.sqlite?_busy_timeout=30000`.
- `NEWAPI_SESSION_SECRET`: explicit session secret; otherwise the script creates a stable local secret.
- `NEWAPI_VERSION`: version injected into the Go binary; otherwise uses `VERSION`, then `git describe`, then `dev`.
- `NEWAPI_DEPLOY_DIR`: runtime output directory; defaults to `.codex-deploy`.
- `NEWAPI_SKIP_FRONTEND_BUILD=1`: skip `bun run build` for both frontends when existing `dist/` assets are acceptable.
- `NEWAPI_BUN_INSTALL=0`: skip `bun install`.
- `NEWAPI_BUN_RETRY_CLEAR_CACHE=0`: disable the automatic Bun cache clear and retry after install failures.
- `NEWAPI_AUTO_SETUP=0`: skip automatic first-run setup.
- `NEWAPI_ADMIN_USERNAME`: initial root admin username; defaults to `admin`.
- `NEWAPI_ADMIN_PASSWORD`: initial root admin password; defaults to `admin123`.
- `NEWAPI_FRONTEND_THEME`: frontend theme to enforce after deployment; defaults to `default` (new frontend). Set to `classic` to use the classic frontend, or `0` to skip theme sync.
- `NEWAPI_SELF_USE_MODE_ENABLED=true`: enable self-use mode during automatic setup; defaults to `false`.
- `NEWAPI_DEMO_SITE_ENABLED=true`: enable demo site mode during automatic setup; defaults to `false`.
- `NEWAPI_QUEUE_TEST_SEED`: after deployment and admin setup, seed local request-queue test data using `.agents/skills/newapi-queue-loadtest/scripts/seed_queue_test_data.py`; defaults to `1`. Set `0` to skip.
- `NEWAPI_QUEUE_TEST_PORTS`: mock OpenAI channel ports for queue tests; defaults to `12000,12001,12003`.
- `NEWAPI_QUEUE_TEST_MOCK_SERVERS`: start local OpenAI-compatible mock upstreams as a daemon before seeding; defaults to `1` when queue test seed is enabled. Set `0` to skip.
- `NEWAPI_QUEUE_TEST_MOCK_TPS`: mock upstream completion speed in tokens per second; defaults to `10`.
- `NEWAPI_QUEUE_TEST_MOCK_OK_COUNT`: mock upstream completion length; defaults to `100` `OK` tokens.
- `NEWAPI_QUEUE_TEST_DEFAULT_CHANNEL_RPM`: fallback channel queue send RPM; defaults to `80`.
- `NEWAPI_QUEUE_TEST_CHANNEL_RPM`: optional per-channel queue send RPM override for seeded channels. Unset by default, so auto-deploy clears `RequestQueueChannelRPM` instead of writing channel-name JSON.
- `NEWAPI_QUEUE_TEST_MAX_CHANNEL_PENDING`: per-channel queue length before 429; defaults to `512`.
- `NEWAPI_QUEUE_TEST_DEFAULT_USER_MAX_PENDING`: per-user queue length across all channels before 429; defaults to `32`.
- `NEWAPI_QUEUE_TEST_SCHEDULE_STRATEGY`: scheduler strategy for queue tests, `fifo` or `user_loop`; defaults to `user_loop`.
- `NEWAPI_QUEUE_TEST_USER_QUOTA_DOLLARS`: minimum balance assigned to each seeded test user; defaults to `10000000`.
- `NEWAPI_QUEUE_TEST_USER_QUOTA`: raw quota-unit override for seeded test users; when set, it takes precedence over `NEWAPI_QUEUE_TEST_USER_QUOTA_DOLLARS`.
- `NEWAPI_QUEUE_TEST_RELAX_RATE_LIMITS`: when queue test seed is enabled, local dashboard API rate limits are relaxed by default so user/channel seeding is not blocked. Set `0` to keep the app defaults.
- `NEWAPI_QUEUE_TEST_TOKENS_FILE`: token output file for the queue load runner; defaults to `.codex-deploy/queue-loadtest/tokens.json`.
- `BUN_BIN`: explicit path to Bun; otherwise the script checks `PATH` and `~/.bun/bin/bun`.

## Behavior

- SQLite is the default backend. Unless `NEWAPI_SQL_DSN` is set, the script
  unsets inherited `SQL_DSN` and `LOG_SQL_DSN` before starting the app.
- Before building, the script checks the target port (`3000` by default). If a
  process is listening there, it asks interactively whether to kill it.
- Existing local processes named `new-api` are stopped before building. The
  script first sends `TERM`, waits briefly, then sends `KILL` only if needed.
- Frontend builds use Bun from `web/default/` and `web/classic/`, matching the
  project convention.
- If `bun install` fails during tarball extraction, scripts clear the Bun cache
  and retry once by default.
- The startup health check calls `/api/status` and prints the log path if the
  process exits or does not become reachable.
- After the health check, the script calls `/api/setup` and creates the initial
  root administrator when the system has not been initialized yet. By default
  it creates `admin` with password `admin123`; override these values for any
  non-throwaway local deployment.
- After setup, the script logs in with the configured administrator and writes
  `theme.frontend=default` so local auto-deploy uses the new frontend by default.
  Use `NEWAPI_FRONTEND_THEME=classic` to switch back for compatibility checks.
- Unless `NEWAPI_QUEUE_TEST_SEED=0` is set, the script then enables request queueing,
  starts local OpenAI-compatible mock upstreams on `NEWAPI_QUEUE_TEST_PORTS`,
  registers matching channels, creates/updates test users, gives each test user
  at least `$10,000,000` local test balance, sets the scheduler
  to low-RPM-first, caps each channel queue at 512 pending requests, and writes
  a token file for queue load tests.
- Before starting mock upstreams, the script automatically kills any process
  listening on the configured mock ports and also stops the previous mock
  process recorded in `.codex-deploy/run/mock-openai.pid`.

## When Running Manually

If deployment fails, inspect:

```bash
tail -n 120 .codex-deploy/logs/new-api.log
tail -n 120 .codex-deploy/logs/mock-openai.log
```

Do not edit protected project identity, branding, module paths, or attribution
while using this skill.

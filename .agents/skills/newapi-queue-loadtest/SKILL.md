---
name: newapi-queue-loadtest
description: Create and run local request-queue load tests for this new-api repository. Use when Codex needs to start OpenAI-compatible mock upstream servers, seed local queue-test channels/users/tokens, integrate the queue seed step with local auto-deploy, or run concurrent per-user RPM traffic to validate request queueing, channel RPM pacing, and scheduler priority behavior.
---

# new-api Queue Load Test

Use this skill for local, disposable request queue experiments. It provides four scripts:

- `scripts/mock_openai_servers.py`: start one or more OpenAI-compatible mock upstreams on `127.0.0.1:<port>`.
  POST requests ignore the input body and return 100 `OK` tokens by default for both streaming and non-streaming calls, paced at 10 tokens/s unless overridden.
- `scripts/seed_queue_test_data.py`: enable queue settings, create/update local channels, create/update test users, and write API tokens for load testing. By default it uses `RequestQueueDefaultChannelRPM` and writes `RequestQueueChannelRPM` as `{}`; pass `--channel-rpm` only when you intentionally want per-channel JSON overrides.
- `scripts/run_queue_loadtest.py`: run concurrent OpenAI chat requests from many users at configurable offered RPMs. By default it randomly mixes streaming and non-streaming requests.
  Non-2xx responses are summarized at the end with sampled response bodies so 502/429 causes can be diagnosed from the terminal output.
- `scripts/test_queue_disabled_smoke.py`: disable request scheduling, send a small mixed stream/non-stream request batch, and assert the selected test users do not enter the request queue.

## Workflow

1. Deploy new-api and seed queue test data. The auto-deploy script starts the mock upstreams as a daemon by default, kills any old process listening on the mock ports, and then seeds matching channels/users/tokens:

```bash
NEWAPI_QUEUE_TEST_PORTS=12000,12001,12003 \
PATH=/usr/local/go/bin:$PATH BUN_BIN="$HOME/.bun/bin/bun" \
.agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh --port 3000
```

The mock process writes:

- PID: `.codex-deploy/run/mock-openai.pid`
- log: `.codex-deploy/logs/mock-openai.log`

To run mock upstreams manually instead:

```bash
python3 .agents/skills/newapi-queue-loadtest/scripts/mock_openai_servers.py 12000 12001 12003
```

2. Run the load test with the token file written by the seed step:

```bash
python3 .agents/skills/newapi-queue-loadtest/scripts/run_queue_loadtest.py \
  --base-url http://127.0.0.1:3000 \
  --tokens-file .codex-deploy/queue-loadtest/tokens.json \
  --duration 120 \
  --rpm-pattern 5 \
  --timeout 900
```

## Defaults

- Mock upstream IP: `127.0.0.1`
- Mock upstream ports: `12000,12001,12003`
- Model: `gpt-4o-mini`
- Mock completion text: 100 `OK` tokens
- Mock completion speed: 10 tokens/s
- Test users: `test1` through `test64`
- Test user password: `test123456`
- Test user balance: at least `$10,000,000` each, unless `NEWAPI_QUEUE_TEST_USER_QUOTA` or `NEWAPI_QUEUE_TEST_USER_QUOTA_DOLLARS` overrides it.
- Per-user offered load: `5` RPM
- Stream mix: random, with roughly 50% streaming and 50% non-streaming requests. Use `--stream-ratio` to change the streaming probability, `--stream-mode stream` for all streaming, or `--stream-mode non-stream` for all non-streaming. The legacy `--stream` flag still forces all streaming.
- Default channel queue RPM: `80`
- Per-channel queue RPM JSON: `{}` by default. Set `NEWAPI_QUEUE_TEST_CHANNEL_RPM` or pass `--channel-rpm` to intentionally write channel-name overrides such as `{"newapi-queue-12000":80}`.
- Scheduler strategy: `user_loop` by default; use `fifo` to preserve strict arrival order.
- Max queued requests per channel: `512`
- Max queued requests per user across all channels: `32`
- Request timeout in the load runner: `900` seconds, so queued requests have time to drain and write consume logs.
- Request queue: enabled, so configured queue RPM limits pace requests through the local queue.
- Token file: `.codex-deploy/queue-loadtest/tokens.json`

The seed script is idempotent: rerunning it updates existing `newapi-queue-<port>` channels and `test<N>` users instead of intentionally creating duplicates.
Channel RPM overrides are keyed by channel name in system settings JSON, so no channel schema field is required. The default test profile leaves that JSON as `{}` and uses the global default channel RPM instead.

## Standard Queue Test Profile

Use this profile when you need the test users' requests to appear in `/usage-logs/common` shortly after the run:

```bash
PATH=/usr/local/go/bin:$PATH BUN_BIN="$HOME/.bun/bin/bun" \
.agents/skills/newapi-autodeploy/scripts/deploy-newapi.sh --port 3000

python3 .agents/skills/newapi-queue-loadtest/scripts/run_queue_loadtest.py \
  --base-url http://127.0.0.1:3000 \
  --tokens-file .codex-deploy/queue-loadtest/tokens.json \
  --limit-users 64 \
  --duration 120 \
  --rpm-pattern 5 \
  --max-in-flight-per-user 8 \
  --timeout 900
```

With the default 64 users and `5` RPM each, offered load is `320` RPM. Three channels at `80` RPM each dispatch `240` RPM, so the scheduler queues requests but should not hit the `512` pending cap during a 120-second standard run. Consume logs are written only after requests complete, so refresh `/usage-logs/common` after the load runner prints its final summary. Filter by type `Consume` and username prefix `test` when logged in as an admin.

## User Loop Tests

To test per-user round-robin scheduling, seed with the user-loop scheduler:

```bash
python3 .agents/skills/newapi-queue-loadtest/scripts/seed_queue_test_data.py \
  --base-url http://127.0.0.1:3000 \
  --ports 12000,12001,12003 \
  --schedule-strategy user_loop
```

Then run a mixed offered load pattern, for example `--rpm-pattern 2,8,16,32`, and confirm each queued user gets turns in rotation while every user's earliest queued request is sent first.

## Queue Disabled Smoke Test

Use this when validating that normal forwarding still works with request scheduling turned off:

```bash
python3 .agents/skills/newapi-queue-loadtest/scripts/test_queue_disabled_smoke.py \
  --base-url http://127.0.0.1:3000 \
  --tokens-file .codex-deploy/queue-loadtest/tokens.json \
  --limit-users 4 \
  --requests 8 \
  --timeout 180
```

The script sets `RequestQueueEnabled=false` and, unless `--keep-model-rate-limit` is passed, also sets `ModelRequestRateLimitEnabled=false` so the smoke test isolates the direct relay path. It then checks `/api/request_queue/logs` before and after the requests and fails if the selected test users remain queued.

## Script Notes

- `seed_queue_test_data.py` uses the admin account from auto-deploy by default: `admin` / `admin123`.
- With local SQLite, token creation uses the database directly to avoid the critical rate limit around token-key reveal endpoints. Use `--token-mode api` only when testing against a non-SQLite deployment and the critical rate limit has been accounted for.
- Mock upstreams log per-port request counts, which is the easiest way to see whether new-api distributes requests across the three channels.
- Do not use these scripts against production data; they create disposable users, channels, and API tokens.

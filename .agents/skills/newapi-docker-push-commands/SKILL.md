---
name: newapi-docker-push-commands
description: Provide and maintain the bundled Docker publish script for building this new-api repository and pushing it to Docker Hub repository sheephuan/newapi. Use when the user asks for the new-api Docker publish script, a versioned Docker publish command, or instructions for running the script with only a version argument.
---

# NewAPI Docker Push Commands

## Core Behavior

Provide the bundled script command for the user to execute. Do not run Docker build, Docker push, or registry mutation commands on the user's behalf unless the user explicitly asks Codex to execute the publish.

Target Docker Hub image:

```text
sheephuan/newapi
```

Use the repository root as the build context. The root `Dockerfile` requires a `VERSION` file, so emitted commands must write the requested version into `VERSION` before building.

Preserve protected project branding and metadata. Do not edit README files, package metadata, module paths, import paths, Dockerfile branding, or other protected project identity references while preparing publish commands.

## Version Handling

If the user provides a version, use it exactly as the Docker tag value after basic shell quoting.

If the user does not provide a version, ask for the version before emitting publish commands. Accept examples such as:

```text
v0.10.8
v0.10.8-alpha.3
nightly-20260514-a1b2c3d
```

## Script First

For a normal publish request, tell the user to run the bundled publish script with exactly one argument, the requested version:

```bash
.agents/skills/newapi-docker-push-commands/scripts/publish-image.sh <VERSION>
```

Do not add unrelated CI/CD analysis.

The script validates that the version is a Docker-compatible tag, writes it to the repository `VERSION` file for the duration of the build, creates or selects a buildx builder, builds a `linux/amd64` image without reusing stale layer cache, pushes both `<VERSION>` and `latest`, inspects the pushed manifests, and restores the previous `VERSION` file afterward.

Prefer `linux/amd64` for this local script because local x86 machines build `linux/arm64` through QEMU. This repository's frontend dependency install can fail under QEMU with Bun native binding packages such as `@rspack/binding-linux-arm64-musl`. For multi-arch release builds, use native amd64 and native arm64 machines or GitHub Actions-style split builds.

The script intentionally does not run `docker login`. Assume Docker Hub login is already complete before the script runs.

If `HTTP_PROXY`, `HTTPS_PROXY`, or `ALL_PROXY` are present in the shell environment, the script passes them into the BuildKit builder container and as Docker build args. It passes `NO_PROXY` only as a build arg because Docker Buildx driver options parse comma-separated values, which breaks common `NO_PROXY` lists such as `.modelscope.cn`. When proxy variables are present, the script recreates the named builder so stale no-proxy BuildKit containers do not keep being reused. It also uses host networking for the builder and build, so Linux host proxies bound to `127.0.0.1` or `localhost` can be reached from the BuildKit container.

## Default Publish Command

For version `20260514`, output:

```bash
.agents/skills/newapi-docker-push-commands/scripts/publish-image.sh 20260514
```

## Optional Variants

If the user asks to avoid updating `latest`, omit the `-t "$IMAGE:latest"` tag and the final `latest` inspect command.

If the user asks for local build only, explain that the bundled script is for pushing to Docker Hub; provide a one-off command separately rather than changing the publish script:

```bash
docker buildx build --platform linux/amd64 -t "sheephuan/newapi:<VERSION>" --load .
```

If the user asks about login, explain that login is intentionally outside the script. A one-off login command can be:

```bash
export DOCKERHUB_USERNAME="sheephuan"
export DOCKERHUB_TOKEN="<paste-docker-hub-access-token>"
printf '%s\n' "$DOCKERHUB_TOKEN" | docker login -u "$DOCKERHUB_USERNAME" --password-stdin
```

## Final Response Style

Keep the answer concise. Include:

- The target image name.
- The version tag being published.
- The script command block.
- A one-line note that Docker Hub login should already be complete.

Do not include unrelated CI/CD analysis unless the user asks for it.

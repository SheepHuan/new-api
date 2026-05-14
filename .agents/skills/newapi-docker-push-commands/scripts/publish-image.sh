#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <version>" >&2
  echo "Example: $0 20260514" >&2
}

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

VERSION_ARG="$1"
IMAGE="sheephuan/newapi"
BUILDER="newapi-builder"
PLATFORM="linux/amd64"

proxy_value() {
  local upper="$1"
  local lower="$2"
  local value="${!upper:-}"
  if [ -z "$value" ]; then
    value="${!lower:-}"
  fi
  printf '%s' "$value"
}

if [ ${#VERSION_ARG} -gt 128 ]; then
  echo "Error: Docker tag must be 128 characters or fewer." >&2
  exit 2
fi

if [[ ! "$VERSION_ARG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]]; then
  echo "Error: version must be a valid Docker tag: [A-Za-z0-9_][A-Za-z0-9_.-]*" >&2
  exit 2
fi

command -v docker >/dev/null 2>&1 || {
  echo "Error: docker is not installed or not in PATH." >&2
  exit 1
}

cd /home/yanghuan/Code/new-api

VERSION_BAK="$(mktemp)"
HAD_VERSION=0
if [ -f VERSION ]; then
  HAD_VERSION=1
  cp VERSION "$VERSION_BAK"
fi

restore_version() {
  if [ "$HAD_VERSION" = "1" ]; then
    cp "$VERSION_BAK" VERSION
  else
    rm -f VERSION
  fi
  rm -f "$VERSION_BAK"
}
trap restore_version EXIT

printf '%s\n' "$VERSION_ARG" > VERSION

HTTP_PROXY_VALUE="$(proxy_value HTTP_PROXY http_proxy)"
HTTPS_PROXY_VALUE="$(proxy_value HTTPS_PROXY https_proxy)"
ALL_PROXY_VALUE="$(proxy_value ALL_PROXY all_proxy)"
NO_PROXY_VALUE="$(proxy_value NO_PROXY no_proxy)"

BUILDER_OPTS=()
if [ -n "$HTTP_PROXY_VALUE" ]; then
  BUILDER_OPTS+=(--driver-opt "env.http_proxy=$HTTP_PROXY_VALUE")
  BUILDER_OPTS+=(--driver-opt "env.HTTP_PROXY=$HTTP_PROXY_VALUE")
fi
if [ -n "$HTTPS_PROXY_VALUE" ]; then
  BUILDER_OPTS+=(--driver-opt "env.https_proxy=$HTTPS_PROXY_VALUE")
  BUILDER_OPTS+=(--driver-opt "env.HTTPS_PROXY=$HTTPS_PROXY_VALUE")
fi
if [ -n "$ALL_PROXY_VALUE" ]; then
  BUILDER_OPTS+=(--driver-opt "env.all_proxy=$ALL_PROXY_VALUE")
  BUILDER_OPTS+=(--driver-opt "env.ALL_PROXY=$ALL_PROXY_VALUE")
fi
if [ "${#BUILDER_OPTS[@]}" -gt 0 ]; then
  BUILDER_OPTS+=(--driver-opt "network=host")
  docker buildx rm "$BUILDER" >/dev/null 2>&1 || true
  docker buildx create --name "$BUILDER" --use "${BUILDER_OPTS[@]}"
else
  docker buildx create --name "$BUILDER" --use 2>/dev/null || docker buildx use "$BUILDER"
fi

docker buildx build \
  --platform "$PLATFORM" \
  --network host \
  --no-cache \
  --build-arg "HTTP_PROXY=$HTTP_PROXY_VALUE" \
  --build-arg "HTTPS_PROXY=$HTTPS_PROXY_VALUE" \
  --build-arg "ALL_PROXY=$ALL_PROXY_VALUE" \
  --build-arg "NO_PROXY=$NO_PROXY_VALUE" \
  --build-arg "http_proxy=$HTTP_PROXY_VALUE" \
  --build-arg "https_proxy=$HTTPS_PROXY_VALUE" \
  --build-arg "all_proxy=$ALL_PROXY_VALUE" \
  --build-arg "no_proxy=$NO_PROXY_VALUE" \
  -t "$IMAGE:$VERSION_ARG" \
  -t "$IMAGE:latest" \
  --push \
  .

docker buildx imagetools inspect "$IMAGE:$VERSION_ARG"
docker buildx imagetools inspect "$IMAGE:latest"

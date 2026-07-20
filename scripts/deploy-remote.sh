#!/usr/bin/env bash

set -Eeuo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

REMOTE_HOST="${REMOTE_HOST:-root@192.168.29.99}"
REMOTE_PORT="${REMOTE_PORT:-19999}"
REMOTE_PATH="${REMOTE_PATH:-/usr/local/bin/stacker}"
CONNECT_TIMEOUT="${CONNECT_TIMEOUT:-10}"
SKIP_TESTS="${SKIP_TESTS:-0}"

usage() {
	cat <<'EOF'
Build and deploy Stacker to a remote machine over SSH.

Usage:
  ./scripts/deploy-remote.sh

Optional environment variables:
  REMOTE_HOST       SSH destination (default: root@192.168.29.99)
  REMOTE_PORT       SSH port (default: 19999)
  REMOTE_PATH       Installation path (default: /usr/local/bin/stacker)
  CONNECT_TIMEOUT   SSH connection timeout in seconds (default: 10)
  SKIP_TESTS=1      Skip mise run test

Examples:
  ./scripts/deploy-remote.sh
  REMOTE_HOST=root@server REMOTE_PORT=22 ./scripts/deploy-remote.sh
  REMOTE_PATH=/root/.local/bin/stacker ./scripts/deploy-remote.sh
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	usage
	exit 0
fi

if [[ $# -ne 0 ]]; then
	usage >&2
	exit 2
fi

for command_name in mise ssh scp sha256sum; do
	if ! command -v "$command_name" >/dev/null 2>&1; then
		printf 'Required command not found: %s\n' "$command_name" >&2
		exit 1
	fi
done

if [[ ! "$REMOTE_PORT" =~ ^[0-9]+$ ]]; then
	printf 'REMOTE_PORT must be numeric: %s\n' "$REMOTE_PORT" >&2
	exit 1
fi

if [[ ! "$CONNECT_TIMEOUT" =~ ^[0-9]+$ ]]; then
	printf 'CONNECT_TIMEOUT must be numeric: %s\n' "$CONNECT_TIMEOUT" >&2
	exit 1
fi

if [[ ! "$REMOTE_PATH" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
	printf 'REMOTE_PATH must be an absolute path without spaces: %s\n' "$REMOTE_PATH" >&2
	exit 1
fi

SSH_OPTIONS=(
	-p "$REMOTE_PORT"
	-o BatchMode=yes
	-o "ConnectTimeout=$CONNECT_TIMEOUT"
	-o StrictHostKeyChecking=accept-new
)

SCP_OPTIONS=(
	-P "$REMOTE_PORT"
	-o BatchMode=yes
	-o "ConnectTimeout=$CONNECT_TIMEOUT"
	-o StrictHostKeyChecking=accept-new
)

printf 'Inspecting %s...\n' "$REMOTE_HOST"
remote_info="$(
	ssh "${SSH_OPTIONS[@]}" "$REMOTE_HOST" \
		'printf "%s %s\n" "$(uname -s)" "$(uname -m)"'
)"
read -r remote_system remote_machine <<<"$remote_info"

case "$remote_system" in
	Linux) target_os=linux ;;
	Darwin) target_os=darwin ;;
	*)
		printf 'Unsupported remote operating system: %s\n' "$remote_system" >&2
		exit 1
		;;
esac

case "$remote_machine" in
	x86_64 | amd64) target_arch=amd64 ;;
	aarch64 | arm64) target_arch=arm64 ;;
	*)
		printf 'Unsupported remote architecture: %s\n' "$remote_machine" >&2
		exit 1
		;;
esac

printf 'Remote target: %s/%s\n' "$target_os" "$target_arch"

if [[ "$SKIP_TESTS" != "1" ]]; then
	printf 'Running tests...\n'
	mise run test
fi

build_path="bin/stacker-${target_os}-${target_arch}"
mkdir -p bin

printf 'Building %s...\n' "$build_path"
mise exec -- env \
	CGO_ENABLED=0 \
	GOOS="$target_os" \
	GOARCH="$target_arch" \
	go build \
	-trimpath \
	-ldflags='-s -w' \
	-o "$build_path" \
	./cmd/stacker

local_checksum="$(sha256sum "$build_path" | awk '{print $1}')"
remote_upload="/tmp/stacker-upload-${local_checksum:0:12}"

printf 'Uploading to %s:%s...\n' "$REMOTE_HOST" "$remote_upload"
scp "${SCP_OPTIONS[@]}" "$build_path" "$REMOTE_HOST:$remote_upload"

printf 'Installing as %s...\n' "$REMOTE_PATH"
# The validated values below are intentionally expanded into remote positional arguments.
# shellcheck disable=SC2029
ssh "${SSH_OPTIONS[@]}" "$REMOTE_HOST" \
	"sh -s -- '$remote_upload' '$REMOTE_PATH' '$local_checksum'" <<'REMOTE_SCRIPT'
set -eu

upload_path=$1
destination=$2
expected_checksum=$3
staged_path="${destination}.new.$$"

cleanup() {
	rm -f "$upload_path" "$staged_path"
}
trap cleanup EXIT HUP INT TERM

chmod 0755 "$upload_path"
"$upload_path" -h >/dev/null 2>&1

install -m 0755 -o root -g root "$upload_path" "$staged_path"
actual_checksum=$(sha256sum "$staged_path" | awk '{print $1}')

if [ "$actual_checksum" != "$expected_checksum" ]; then
	printf 'Checksum mismatch: expected %s, got %s\n' "$expected_checksum" "$actual_checksum" >&2
	exit 1
fi

mv -f "$staged_path" "$destination"
"$destination" -h >/dev/null 2>&1
printf 'Installed %s\nChecksum: %s\n' "$destination" "$actual_checksum"
REMOTE_SCRIPT

printf 'Deployment completed successfully.\n'

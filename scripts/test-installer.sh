#!/usr/bin/env bash

set -Eeuo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPORARY_DIRECTORY="$(mktemp -d)"

cleanup() {
	rm -rf "$TEMPORARY_DIRECTORY"
}
trap cleanup EXIT

case "$(uname -s)" in
	Linux) target_os=linux ;;
	Darwin) target_os=darwin ;;
	*)
		printf 'Unsupported test operating system\n' >&2
		exit 1
		;;
esac

case "$(uname -m)" in
	x86_64 | amd64) target_arch=amd64 ;;
	aarch64 | arm64) target_arch=arm64 ;;
	*)
		printf 'Unsupported test architecture\n' >&2
		exit 1
		;;
esac

asset="stacker-${target_os}-${target_arch}"
release_directory="${TEMPORARY_DIRECTORY}/release"
mock_directory="${TEMPORARY_DIRECTORY}/mock-bin"
install_directory="${TEMPORARY_DIRECTORY}/install"
mkdir -p "$release_directory" "$mock_directory"

cat > "${release_directory}/${asset}" <<'FAKE_BINARY'
#!/bin/sh

if [ "${1:-}" = "-h" ]; then
	exit 0
fi
printf 'fake stacker\n'
FAKE_BINARY
chmod 0755 "${release_directory}/${asset}"

if command -v sha256sum >/dev/null 2>&1; then
	(
		cd "$release_directory"
		sha256sum "$asset" > checksums.txt
	)
else
	checksum=$(shasum -a 256 "${release_directory}/${asset}")
	printf '%s  %s\n' "${checksum%% *}" "$asset" > "${release_directory}/checksums.txt"
fi

cat > "${mock_directory}/curl" <<'MOCK_CURL'
#!/bin/sh

url=
output=
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o)
			output=$2
			shift 2
			;;
		https://*)
			url=$1
			shift
			;;
		*) shift ;;
	esac
done

[ -n "$url" ] && [ -n "$output" ]
cp "${STACKER_TEST_RELEASE_DIRECTORY}/${url##*/}" "$output"
MOCK_CURL
chmod 0755 "${mock_directory}/curl"

STACKER_TEST_RELEASE_DIRECTORY="$release_directory" \
	STACKER_INSTALL_DIR="$install_directory" \
	STACKER_VERSION=v0.0.0 \
	PATH="${mock_directory}:${PATH}" \
	/bin/sh "${PROJECT_ROOT}/install.sh"

installed_binary="${install_directory}/stacker"
if [ ! -x "$installed_binary" ]; then
	printf 'Installer did not create an executable binary\n' >&2
	exit 1
fi
"$installed_binary" -h
printf 'Installer integration test passed\n'

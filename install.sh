#!/bin/sh

set -eu

REPOSITORY=${STACKER_REPOSITORY:-tarcisiomiranda/stacker}
VERSION=${STACKER_VERSION:-latest}
INSTALL_DIR=${STACKER_INSTALL_DIR:-}
BINARY_NAME=stacker

fail() {
	printf 'stacker installer: %s\n' "$*" >&2
	exit 1
}

command_exists() {
	command -v "$1" >/dev/null 2>&1
}

command_exists curl || fail "curl is required"
command_exists install || fail "install is required"

case "$(uname -s)" in
	Linux) target_os=linux ;;
	Darwin) target_os=darwin ;;
	*) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64) target_arch=amd64 ;;
	aarch64 | arm64) target_arch=arm64 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
esac

asset="${BINARY_NAME}-${target_os}-${target_arch}"
if [ "$VERSION" = latest ]; then
	download_url="https://github.com/${REPOSITORY}/releases/latest/download"
else
	download_url="https://github.com/${REPOSITORY}/releases/download/${VERSION}"
fi

temporary_directory=$(mktemp -d 2>/dev/null || mktemp -d -t stacker-install)
cleanup() {
	rm -rf "$temporary_directory"
}
trap cleanup EXIT HUP INT TERM

printf 'Downloading %s (%s/%s)...\n' "$BINARY_NAME" "$target_os" "$target_arch"
curl --proto '=https' --tlsv1.2 -fsSL \
	"${download_url}/${asset}" \
	-o "${temporary_directory}/${asset}"
curl --proto '=https' --tlsv1.2 -fsSL \
	"${download_url}/checksums.txt" \
	-o "${temporary_directory}/checksums.txt"

expected_checksum=
while read -r checksum filename _; do
	filename=${filename#\*}
	if [ "$filename" = "$asset" ]; then
		expected_checksum=$checksum
		break
	fi
done < "${temporary_directory}/checksums.txt"

[ -n "$expected_checksum" ] || fail "checksum for ${asset} was not found"

if command_exists sha256sum; then
	checksum_output=$(sha256sum "${temporary_directory}/${asset}")
elif command_exists shasum; then
	checksum_output=$(shasum -a 256 "${temporary_directory}/${asset}")
else
	fail "sha256sum or shasum is required"
fi
actual_checksum=${checksum_output%% *}

if [ "$actual_checksum" != "$expected_checksum" ]; then
	fail "checksum mismatch for ${asset}"
fi
printf 'Checksum verified: %s\n' "$actual_checksum"

if [ -z "$INSTALL_DIR" ]; then
	if [ "$(id -u)" -eq 0 ] || [ -w /usr/local/bin ]; then
		INSTALL_DIR=/usr/local/bin
	else
		INSTALL_DIR=${HOME}/.local/bin
	fi
fi

target_path="${INSTALL_DIR}/${BINARY_NAME}"
if mkdir -p "$INSTALL_DIR" 2>/dev/null && [ -w "$INSTALL_DIR" ]; then
	install -m 0755 "${temporary_directory}/${asset}" "$target_path"
elif command_exists sudo; then
	printf 'Administrator permission is required to install in %s.\n' "$INSTALL_DIR"
	sudo mkdir -p "$INSTALL_DIR"
	sudo install -m 0755 "${temporary_directory}/${asset}" "$target_path"
else
	fail "cannot write to ${INSTALL_DIR}; set STACKER_INSTALL_DIR to a writable directory"
fi

"$target_path" -h >/dev/null 2>&1 || fail "installed binary did not start correctly"

printf 'Stacker installed successfully: %s\n' "$target_path"
case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) ;;
	*) printf 'Add %s to PATH before running stacker.\n' "$INSTALL_DIR" ;;
esac

# Stacker

Terminal process supervisor written in Go and configured with YAML.

- processes defined in YAML;
- graceful start, stop, and restart;
- separate stdout and stderr capture;
- scrollable logs with a configurable memory limit;
- drag selection and copying through OSC 52;
- termination of the entire process group on Linux and macOS.

## Quick installation

```bash
curl -fsSL https://raw.githubusercontent.com/tarcisiomiranda/stacker/main/install.sh | bash
```

The installer detects the operating system and architecture, downloads the latest release, and verifies its SHA-256 checksum. When run as `root`, it installs Stacker in `/usr/local/bin`. For other users, it falls back to `~/.local/bin` when `/usr/local/bin` is not writable.

To choose an installation directory or a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/tarcisiomiranda/stacker/main/install.sh \
  | STACKER_INSTALL_DIR="$HOME/.local/bin" STACKER_VERSION=v0.1.0 bash
```

## Run

```bash
stacker -config stacker.yml
```

Without `-config`, Stacker looks for `stacker.yml` in the current directory.

Relative `cwd` paths are resolved from the directory containing the configuration file. Unknown YAML fields, empty commands, missing directories, and invalid timeouts are rejected before the TUI opens.

Each command inherits the environment, including `PATH`, from the user who started Stacker and runs through `/bin/sh -c`. If `mise` already works in the current terminal, use `command: mise run task-name`; there is no need to export paths or activate `mise` inside the YAML file.

## Development

```bash
mise install
mise run dev
```

The `demo` process starts automatically so you can test log handling without configuring another project.

## Local build

```bash
mise run build
./bin/stacker -config stacker.yml
```

## Install from source

```bash
mise run install
```

`go install` writes the binary to `$GOBIN` or, when it is not configured, to `$(go env GOPATH)/bin`.

## Releases

The `.github/workflows/release.yml` workflow publishes releases for SemVer tags. It runs tests and `go vet`, builds CGO-disabled binaries for Linux and macOS on AMD64 and ARM64, generates `checksums.txt`, and attaches all artifacts to the GitHub Release.

To publish a version:

```bash
git tag v0.1.0
git push origin v0.1.0
```

You can also start the workflow manually from the Actions tab and provide the desired tag. The pipeline does not create commits or modify the `main` branch.

## Controls

- `↑/↓` or `j/k`: select a process
- `Enter`: start
- `s`: stop
- `r`: restart
- mouse wheel: scroll through logs
- drag with the left mouse button: select lines
- release the left mouse button: copy the selection
- `G` or `End`: return to the bottom
- `Esc`: clear the selection
- `q`: quit

## Current limitations

- commands run through `/bin/sh -c`, so Stacker currently targets Linux and macOS;
- selection operates on entire lines, not individual columns;
- OSC 52 depends on terminal support and configuration;
- process health checks and dependencies are not supported yet.

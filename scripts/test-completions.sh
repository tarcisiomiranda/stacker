#!/usr/bin/env bash
# Exercise the shell completion scripts against a real supervisor.
#
# Go tests cover `stacker __complete`; this covers the other half — that each
# shell actually parses the script and turns that output into candidates.
# bash and fish are driven non-interactively; zsh is syntax-checked only,
# because its completion system needs a pty to run (see README).
#
# Run: mise run test:completions

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
binary="${repo_root}/bin/stacker"
completions="${repo_root}/cmd/stacker/completions"
failures=0

fail() {
	printf 'FAIL: %s\n' "$1" >&2
	failures=$((failures + 1))
}

pass() {
	printf 'ok: %s\n' "$1"
}

if [ ! -x "$binary" ]; then
	printf 'error: %s not built; run "mise run build" first\n' "$binary" >&2
	exit 1
fi

workdir=$(mktemp -d)
config="${workdir}/stacker.yml"
cat >"$config" <<'YML'
version: 1
processes:
  api:
    command: sh -c 'while :; do sleep 1; done'
    port: 59123
    tasks:
      migrate: "true"
  worker:
    command: "true"
YML

export XDG_RUNTIME_DIR="${workdir}/run"
mkdir -p "$XDG_RUNTIME_DIR"

cleanup() {
	"$binary" --config "$config" down >/dev/null 2>&1 || true
	rm -rf "$workdir"
}
trap cleanup EXIT

"$binary" --config "$config" serve -d >/dev/null 2>&1
# The supervisor needs a moment before it answers the control plane.
for _ in 1 2 3 4 5 6 7 8 9 10; do
	if "$binary" --config "$config" ping >/dev/null 2>&1; then
		break
	fi
	sleep 0.3
done

# --- syntax ------------------------------------------------------------------
for shell in bash zsh; do
	if command -v "$shell" >/dev/null 2>&1; then
		if "$shell" -n "${completions}/stacker.${shell}"; then
			pass "${shell}: script parses"
		else
			fail "${shell}: syntax error"
		fi
	else
		printf 'skip: %s not installed\n' "$shell"
	fi
done

# --- bash: drive the completion function directly -----------------------------
if command -v bash >/dev/null 2>&1; then
	bash_out=$(
		PATH="${repo_root}/bin:$PATH" bash -c '
			source "$1"
			COMP_WORDS=(stacker --config "$2" logs "")
			COMP_CWORD=4
			_stacker_complete
			printf "%s\n" "${COMPREPLY[@]}"
		' _ "${completions}/stacker.bash" "$config"
	)
	if grep -qx 'api' <<<"$bash_out" && grep -qx 'worker' <<<"$bash_out"; then
		pass "bash: logs completes process names"
	else
		fail "bash: logs completion returned '${bash_out}'"
	fi
	# Descriptions must not leak into bash candidates.
	if grep -q '·' <<<"$bash_out"; then
		fail "bash: descriptions leaked into candidates"
	else
		pass "bash: descriptions stripped"
	fi

	bash_cmds=$(
		PATH="${repo_root}/bin:$PATH" bash -c '
			source "$1"
			COMP_WORDS=(stacker "")
			COMP_CWORD=1
			_stacker_complete
			printf "%s\n" "${COMPREPLY[@]}"
		' _ "${completions}/stacker.bash"
	)
	if grep -qx 'logs' <<<"$bash_cmds"; then
		pass "bash: bare stacker completes commands"
	else
		fail "bash: command completion returned '${bash_cmds}'"
	fi
fi

# --- fish: `complete -C` is the real completion path ---------------------------
if command -v fish >/dev/null 2>&1; then
	fish_out=$(fish -c "
		set -gx PATH ${repo_root}/bin \$PATH
		source ${completions}/stacker.fish
		complete -C 'stacker --config ${config} logs '
	")
	if grep -q '^api	' <<<"$fish_out"; then
		pass "fish: logs completes processes with descriptions"
	else
		fail "fish: logs completion returned '${fish_out}'"
	fi

	fish_cmds=$(fish -c "
		set -gx PATH ${repo_root}/bin \$PATH
		source ${completions}/stacker.fish
		complete -C 'stacker '
	")
	if grep -q '^logs	' <<<"$fish_cmds"; then
		pass "fish: bare stacker completes commands"
	else
		fail "fish: command completion returned '${fish_cmds}'"
	fi

	fish_tasks=$(fish -c "
		set -gx PATH ${repo_root}/bin \$PATH
		source ${completions}/stacker.fish
		complete -C 'stacker --config ${config} run api '
	")
	if grep -q '^migrate	' <<<"$fish_tasks"; then
		pass "fish: run completes the tasks of that process"
	else
		fail "fish: task completion returned '${fish_tasks}'"
	fi
fi

if [ "$failures" -gt 0 ]; then
	printf '\n%d check(s) failed\n' "$failures" >&2
	exit 1
fi
printf '\nall completion checks passed\n'

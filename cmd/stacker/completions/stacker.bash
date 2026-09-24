# bash completion for stacker
#
# Install:
#   stacker completion bash > ~/.local/share/bash-completion/completions/stacker
# bash-completion must be active for that path to load; without it, source this
# file from ~/.bashrc directly.
#
# Candidates come from `stacker __complete`, so process names are the ones the
# running supervisor actually has. bash shows no descriptions, so the tab-
# separated hint is stripped here.

_stacker_candidates() {
	# $1 = kind, $2… = extra args. Never let a failure leak to the prompt.
	local config_flag=()
	if [ -n "$_stacker_config" ]; then
		config_flag=(--config "$_stacker_config")
	fi
	"${_stacker_bin}" "${config_flag[@]}" __complete "$@" 2>/dev/null | cut -f1
}

_stacker_group_candidates() {
	local candidate
	while IFS= read -r candidate; do
		[[ "$candidate" == "$current"* ]] && COMPREPLY+=("$candidate")
	done < <(_stacker_candidates groups)
}

_stacker_complete() {
	local current previous words index
	COMPREPLY=()
	current="${COMP_WORDS[COMP_CWORD]}"
	previous="${COMP_WORDS[COMP_CWORD - 1]}"
	_stacker_bin="${COMP_WORDS[0]}"
	_stacker_config=""

	# Walk the line once: pick up --config (completions must describe the
	# instance the command will talk to) and the first bare word as the command.
	local command="" process=""
	local -a positional=()
	for ((index = 1; index < COMP_CWORD; index++)); do
		words="${COMP_WORDS[index]}"
		case "$words" in
		--config | -config)
			_stacker_config="${COMP_WORDS[index + 1]}"
			((index++))
			;;
		--config=*) _stacker_config="${words#--config=}" ;;
		-n | --tail | --lines | --since | --group | -g) ((index++)) ;;
		-*) ;;
		*) positional+=("$words") ;;
		esac
	done
	if [ ${#positional[@]} -gt 0 ]; then
		command="${positional[0]}"
	fi
	if [ ${#positional[@]} -gt 1 ]; then
		process="${positional[1]}"
	fi

	# A flag that takes a value: complete the value, not another flag.
	case "$previous" in
	--config | -config)
		mapfile -t COMPREPLY < <(compgen -f -- "$current")
		return 0
		;;
	-n | --tail | --lines | --since)
		return 0
		;;
	--group | -g)
		_stacker_group_candidates
		return 0
		;;
	esac

	if [[ "$current" == -* ]]; then
		mapfile -t COMPREPLY < <(compgen -W "$(_stacker_candidates flags "$command")" -- "$current")
		return 0
	fi

	if [ -z "$command" ]; then
		mapfile -t COMPREPLY < <(compgen -W "$(_stacker_candidates commands)" -- "$current")
		return 0
	fi

	case "$command" in
	completion)
		mapfile -t COMPREPLY < <(compgen -W "$(_stacker_candidates shells)" -- "$current")
		;;
	run)
		if [ -z "$process" ]; then
			mapfile -t COMPREPLY < <(compgen -W "$(_stacker_candidates processes)" -- "$current")
		else
			mapfile -t COMPREPLY < <(compgen -W "$(_stacker_candidates tasks "$process")" -- "$current")
		fi
		;;
	logs | log | start | stop | restart | status | tasks)
		# Only the first positional is a process name.
		if [ -z "$process" ]; then
			mapfile -t COMPREPLY < <(compgen -W "$(_stacker_candidates processes)" -- "$current")
		fi
		;;
	esac
	return 0
}

complete -F _stacker_complete stacker

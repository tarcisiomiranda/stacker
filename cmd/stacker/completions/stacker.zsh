#compdef stacker
#
# zsh completion for stacker
#
# Install into a directory already in $fpath, for example:
#   stacker completion zsh > "${fpath[1]}/_stacker"
# On macOS with Homebrew, /opt/homebrew/share/zsh/site-functions is in $fpath
# and is the usual home for this file.
#
# Candidates come from `stacker __complete`, so the process list is whatever
# the running supervisor has, and each one carries its status as a description.

_stacker_candidates() {
	# $1 = kind, $2… = extra args. Fills the caller's `candidates` array with
	# "value:description" pairs that _describe understands.
	local -a config_flag
	[[ -n $_stacker_config ]] && config_flag=(--config "$_stacker_config")

	local line name description
	candidates=()
	while IFS=$'\t' read -r name description; do
		[[ -z $name ]] && continue
		# _describe splits on the first colon, so a colon in the name would
		# truncate the candidate.
		candidates+=("${name//:/\\:}:${description}")
	done < <("${_stacker_bin}" $config_flag __complete "$@" 2>/dev/null)
}

_stacker() {
	local _stacker_bin=${words[1]}
	local _stacker_config=""
	local command="" process=""
	local -a positional candidates
	local index word

	# One pass over the line: --config decides which instance we describe, and
	# the bare words give the command and its first argument.
	for (( index = 2; index < CURRENT; index++ )); do
		word=${words[index]}
		case $word in
			--config|-config)
				_stacker_config=${words[index + 1]}
				(( index++ ))
				;;
			--config=*) _stacker_config=${word#--config=} ;;
			-n|--tail|--lines|--since) (( index++ )) ;;
			-*) ;;
			*) positional+=("$word") ;;
		esac
	done
	command=${positional[1]:-}
	process=${positional[2]:-}

	# Value-taking flags: complete the value.
	case ${words[CURRENT - 1]} in
		--config|-config)
			_files
			return
			;;
		-n|--tail|--lines|--since)
			return
			;;
	esac

	if [[ ${words[CURRENT]} == -* ]]; then
		_stacker_candidates flags "$command"
		_describe -t flags 'flag' candidates
		return
	fi

	if [[ -z $command ]]; then
		_stacker_candidates commands
		_describe -t commands 'stacker command' candidates
		return
	fi

	case $command in
		completion)
			_stacker_candidates shells
			_describe -t shells 'shell' candidates
			;;
		run)
			if [[ -z $process ]]; then
				_stacker_candidates processes
				_describe -t processes 'process' candidates
			else
				_stacker_candidates tasks "$process"
				_describe -t tasks 'task' candidates
			fi
			;;
		logs|log|start|stop|restart|status|tasks)
			# Only the first positional names a process.
			if [[ -z $process ]]; then
				_stacker_candidates processes
				_describe -t processes 'process' candidates
			fi
			;;
	esac
}

_stacker "$@"

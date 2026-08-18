# fish completion for stacker
#
# Install:
#   stacker completion fish > ~/.config/fish/completions/stacker.fish
# fish loads that directory automatically; no further configuration.
#
# Candidates come from `stacker __complete`, whose "value<TAB>description"
# output is exactly what `complete -a` consumes, so process names arrive with
# their status attached.

function __stacker_config
	# The --config on the current line decides which instance we describe.
	set -l tokens (commandline -opc)
	set -l index 1
	while test $index -le (count $tokens)
		switch $tokens[$index]
			case '--config' '-config'
				if test (math $index + 1) -le (count $tokens)
					echo $tokens[(math $index + 1)]
					return 0
				end
			case '--config=*'
				string replace -- '--config=' '' $tokens[$index]
				return 0
		end
		set index (math $index + 1)
	end
end

function __stacker_complete
	set -l config (__stacker_config)
	if test -n "$config"
		command stacker --config $config __complete $argv 2>/dev/null
	else
		command stacker __complete $argv 2>/dev/null
	end
end

function __stacker_positionals
	# Bare words after the binary: [1] is the command, [2] its first argument.
	set -l tokens (commandline -opc)
	set -l out
	set -l index 2
	while test $index -le (count $tokens)
		switch $tokens[$index]
			case '--config' '-config' '-n' '--tail' '--lines' '--since'
				set index (math $index + 1)
			case '-*'
			case '*'
				set -a out $tokens[$index]
		end
		set index (math $index + 1)
	end
	if test (count $out) -gt 0
		printf '%s\n' $out
	end
end

# The helpers below return a status as well as a value: a condition that also
# reports "there is nothing here" is what keeps the first TAB (no command typed
# yet) from being swallowed.
function __stacker_command
	set -l positionals (__stacker_positionals)
	if test (count $positionals) -ge 1
		echo $positionals[1]
		return 0
	end
	return 1
end

function __stacker_process_argument
	set -l positionals (__stacker_positionals)
	if test (count $positionals) -ge 2
		echo $positionals[2]
		return 0
	end
	return 1
end

function __stacker_command_is
	set -l command (__stacker_command)
	or return 1
	contains -- $command $argv
end

function __stacker_no_command
	not __stacker_command >/dev/null
end

function __stacker_wants_process
	__stacker_command_is logs log start stop restart status tasks run
	or return 1
	# Only the first positional is a process name.
	not __stacker_process_argument >/dev/null
end

function __stacker_wants_task
	__stacker_command_is run
	and __stacker_process_argument >/dev/null
end

function __stacker_wants_shell
	__stacker_command_is completion completions
	and not __stacker_process_argument >/dev/null
end

# No bare file completion: every position below is a name, not a path.
complete -c stacker -f

complete -c stacker -n '__stacker_no_command' -a '(__stacker_complete commands)'
complete -c stacker -n '__stacker_wants_process' -a '(__stacker_complete processes)'
complete -c stacker -n '__stacker_wants_task' \
	-a '(__stacker_complete tasks (__stacker_process_argument))'
complete -c stacker -n '__stacker_wants_shell' -a '(__stacker_complete shells)'

complete -c stacker -l config -r -F -d 'Path to stacker.yml'
complete -c stacker -l json -d 'Machine-readable output'

# Log flags, offered only where they mean something.
complete -c stacker -n '__stacker_command_is logs log' \
	-s n -l tail -r -d 'Show the last N lines (default 200)'
complete -c stacker -n '__stacker_command_is logs log' \
	-l since -r -d 'Start at an absolute log index'
complete -c stacker -n '__stacker_command_is logs log' \
	-s f -l follow -d 'Keep printing new lines'
complete -c stacker -n '__stacker_command_is logs log' \
	-l all -d 'Show everything still retained'
complete -c stacker -n '__stacker_command_is logs log' \
	-l supervisor -d "The daemon's own log file"

complete -c stacker -n '__stacker_command_is serve' \
	-s d -l background -d 'Daemonize the supervisor'
complete -c stacker -n '__stacker_command_is serve' \
	-l web -d 'Start the web log viewer too'

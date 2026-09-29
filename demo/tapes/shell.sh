# Sourced (hidden) at the start of every tape, from the repository root:
# loads the sandbox's `lu-cleaner` function (demo/record.sh exports
# LU_DEMO_ENV), sets a short prompt and clears the screen. Kept in a file
# because VHS mistypes non-ASCII characters such as the prompt's ❯.
[ -n "${LU_DEMO_ENV:-}" ] && [ -f "$LU_DEMO_ENV" ] || { echo "LU_DEMO_ENV is not set: run demo/record.sh" >&2; return 1; }
. "$LU_DEMO_ENV"
PS1='\[\e[38;5;183m\]~/code\[\e[0m\] \[\e[38;5;117m\]❯\[\e[0m\] '
clear

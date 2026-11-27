# A stand-in for a coding agent (claude, codex, opencode) in tests: it draws a little of the
# agent's screen and reports its lifecycle through the real hook relay, with the payloads in
# tests/fixtures/hooks/payloads. It is driven by lines typed into its terminal:
#   <anything else>  a prompt: UserPromptSubmit
#   tool             PreToolUse
#   ask              PermissionRequest, then waits for one key; "1", "y" or Enter allows
#   stop             Stop
#   error            Error
#   quit             SessionEnd, exit 0
#   crash            exit 7 without a word
# Usage: bash agent.sh <agent> <fixtures dir> [agent arguments...]
agent=$1 fixtures=$2
shift 2
session="fake-$agent-$$"
report() { # report <Event> [prompt]
  sed -e "s|__SESSION__|$session|g" -e "s|__CWD__|$PWD|g" -e "s|__PROMPT__|${2:-}|g" "$fixtures/$agent.$1.json" |
    devbox-hub event "$agent" "$1"
}
echo "fake $agent ${*:-} (session $session)"
report SessionStart
for argument in "$@"; do
  case $argument in -*|resume) ;; *) report UserPromptSubmit "$argument" ;; esac
done
while IFS= read -r line; do
  case $line in
    tool) report PreToolUse ;;
    ask)
      report PermissionRequest
      echo "Do you want to proceed? 1. Yes  2. Yes, and don't ask again  3. No"
      IFS= read -rsn1 key
      case $key in 1|y|"") echo "allowed"; report PostToolUse ;; *) echo "denied ($key)" ;; esac ;;
    stop) report Stop ;;
    error) report Error ;;
    quit) report SessionEnd; exit 0 ;;
    crash) exit 7 ;;
    *) report UserPromptSubmit "$line" ;;
  esac
done

# shellcheck shell=bash

# Canary library for bash 5.3. Source this file; do not execute it.
# A caller may set CANARY_GOAL to pin one goal from the pool.

# canary_on_exit is the cancel hook. The EXIT trap runs it.
# The default behaviour is to do nothing. Redefine the function after
# sourcing this file to cancel work when the script exits.
canary_on_exit() {
  :
}

trap 'canary_on_exit' EXIT

# canary_goals writes the pool, one synthetic goal per line.
canary_goals() {
  printf '%s\n' \
    'Summarise the synthetic project note for workspace Aster.' \
    'List the open synthetic tasks tagged amber in workspace Birch.' \
    'Draft a checklist for synthetic onboarding in workspace Cedar.'
}

# canary_json_escape writes $1 as a JSON string body.
# Quotes, backslashes and line breaks are escaped. The result has no raw newline.
canary_json_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "$value"
}

# canary_pick writes one goal.
# When CANARY_GOAL is set to a pool goal, that goal is written.
# When CANARY_GOAL is set to anything else, the function returns non-zero.
# When CANARY_GOAL is unset, one pool goal is drawn at random.
canary_pick() {
  local -a pool=()
  local goal
  local count
  local index

  mapfile -t pool < <(canary_goals)
  count=${#pool[@]}
  if (( count == 0 )); then
    printf 'canary_pick: the goal pool is empty\n' >&2
    return 1
  fi

  if [[ -v CANARY_GOAL ]]; then
    for goal in "${pool[@]}"; do
      if [[ "$goal" == "$CANARY_GOAL" ]]; then
        printf '%s\n' "$CANARY_GOAL"
        return 0
      fi
    done
    printf 'canary_pick: CANARY_GOAL is not in the pool\n' >&2
    return 1
  fi

  index=$(( RANDOM % count ))
  printf '%s\n' "${pool[$index]}"
}

# canary_result writes one JSON object as one line.
# Arguments: goal, verdict, tokens, cost, duration.
# tokens is an integer. cost and duration are strings.
canary_result() {
  local goal
  local verdict
  local tokens
  local cost
  local duration
  local escaped_goal
  local escaped_verdict
  local escaped_cost
  local escaped_duration

  if (( $# != 5 )); then
    printf 'canary_result: expected goal, verdict, tokens, cost and duration\n' >&2
    return 1
  fi

  goal=$1
  verdict=$2
  tokens=$3
  cost=$4
  duration=$5

  if [[ ! "$tokens" =~ ^-?(0|[1-9][0-9]*)$ ]]; then
    printf 'canary_result: tokens must be an integer\n' >&2
    return 1
  fi

  escaped_goal=$(canary_json_escape "$goal")
  escaped_verdict=$(canary_json_escape "$verdict")
  escaped_cost=$(canary_json_escape "$cost")
  escaped_duration=$(canary_json_escape "$duration")

  printf '{"goal":"%s","verdict":"%s","tokens":%s,"cost":"%s","duration":"%s"}\n' \
    "$escaped_goal" "$escaped_verdict" "$tokens" "$escaped_cost" "$escaped_duration"
}

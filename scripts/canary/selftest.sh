#!/usr/bin/env bash
# Self-test for scripts/canary/lib.sh.
set -euo pipefail

script_dir=$(dirname -- "${BASH_SOURCE[0]}")
# shellcheck source=canary/lib.sh
source "${script_dir}/lib.sh"

fail() {
  printf 'selftest: %s\n' "$*" >&2
  exit 1
}

assert_json() {
  local want=$1
  shift
  local raw
  local body
  raw=$(canary_result "$@"; printf X)
  body=${raw%X}
  if [[ "$body" != "$want"$'\n' ]]; then
    printf 'selftest: canary_result mismatch\n got:  %s\n want: %s\n' "${body%$'\n'}" "$want" >&2
    exit 1
  fi
}

# Later checks must see CANARY_GOAL unset. Clear any value from the environment.
unset CANARY_GOAL

declare -A pool=()
while IFS= read -r goal; do
  [[ -n "$goal" ]] || fail "blank goal in the pool"
  pool["$goal"]=1
done < <(canary_goals)

if (( ${#pool[@]} < 3 )); then
  fail "pool has ${#pool[@]} distinct goals"
fi

pinned=$(canary_goals)
pinned=${pinned%%$'\n'*}
[[ -n "$pinned" ]] || fail "could not read a goal to pin"

# 30 draws without CANARY_GOAL produce at least 2 distinct goals.
declare -A drawn=()
for ((i = 0; i < 30; i++)); do
  goal=$(canary_pick)
  [[ -n "$goal" ]] || fail "draw ${i} was empty"
  [[ -v pool["$goal"] ]] || fail "draw outside the pool"
  drawn["$goal"]=1
done
if (( ${#drawn[@]} < 2 )); then
  fail "30 draws produced ${#drawn[@]} distinct goals"
fi

# A pinned pool goal is stable.
CANARY_GOAL=$pinned
for ((i = 0; i < 10; i++)); do
  goal=$(canary_pick)
  [[ "$goal" == "$pinned" ]] || fail "pinned draw returned a different goal"
done
unset CANARY_GOAL

# A value outside the pool fails. An empty value is set, and it is not a goal.
CANARY_GOAL='not-a-pool-goal'
if canary_pick >/dev/null 2>&1; then
  unset CANARY_GOAL
  fail "a CANARY_GOAL outside the pool was accepted"
fi
unset CANARY_GOAL

CANARY_GOAL=
if canary_pick >/dev/null 2>&1; then
  unset CANARY_GOAL
  fail "an empty CANARY_GOAL was accepted"
fi
unset CANARY_GOAL

if [[ -v CANARY_GOAL ]]; then
  fail "CANARY_GOAL is still set"
fi

assert_json \
  '{"goal":"Summarize the synthetic note.","verdict":"pass","tokens":12,"cost":"0.0100","duration":"1.25s"}' \
  'Summarize the synthetic note.' pass 12 '0.0100' '1.25s'

assert_json \
  '{"goal":"say \"hi\" \\ path","verdict":"fail","tokens":0,"cost":"0","duration":"0s"}' \
  'say "hi" \ path' fail 0 '0' '0s'

assert_json \
  '{"goal":"a\nb","verdict":"pass","tokens":1,"cost":"1","duration":"1s"}' \
  $'a\nb' pass 1 '1' '1s'

for bad_tokens in '1.5' '01' 'nope'; do
  if canary_result 'goal' pass "$bad_tokens" '0' '0s' >/dev/null 2>&1; then
    fail "tokens value ${bad_tokens} was accepted"
  fi
done

if [[ -v CANARY_GOAL ]]; then
  fail "CANARY_GOAL is still set"
fi

# The EXIT trap runs in a child process. A subshell does not run it.
default_out=$(
  bash --noprofile --norc -c \
    'source "$1"; exit 0' \
    bash "${script_dir}/lib.sh"
)
[[ -z "$default_out" ]] || fail "the default cancel hook wrote output"

hook_out=$(
  bash --noprofile --norc -c \
    'source "$1"; canary_on_exit() { printf "%s\n" canceled; }; exit 0' \
    bash "${script_dir}/lib.sh"
)
[[ "$hook_out" == "canceled" ]] || fail "EXIT trap did not run the cancel hook"

if [[ -v CANARY_GOAL ]]; then
  fail "CANARY_GOAL is still set"
fi

printf 'selftest ok\n'

#!/bin/sh
# Start Postgres, gateway and brain for this checkout's e2e project, and print one
# sign-in link. A second run recovers instead of bootstrapping again. In an
# e2e project (upmo-e2e-*) every run also resets the e2e person and empties the
# identity throttle with e2e/reset.sql, and replaces their tasks with the fixed
# set in e2e/seed-tasks.sql, so it starts from the same state. The default
# project is a dev database and is never reset or seeded.
set -eu

cd "$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"

if [ -z "${UPMO_GIT_DIR:-}" ]; then
  UPMO_GIT_DIR=$(sed -n 's|^gitdir: \(.*\)/worktrees/[^/]*$|\1|p' .git 2>/dev/null || true)
  export UPMO_GIT_DIR
fi

project=${1:?project}
if [ "$project" = "-" ]; then
  dc_args=
else
  dc_args="-p $project -f compose.yml -f e2e/web.override.yml"
  if [ "${E2E_WEB:-}" = "preview" ]; then
    dc_args="$dc_args -f e2e/web.preview.yml"
  fi
fi

dc() {
  # shellcheck disable=SC2086
  docker compose $dc_args "$@"
}

say() {
  printf '%s\n' "$*" >&2
}

dc up -d --wait postgres

issue() {
  out=$(mktemp)
  if ! dc run --rm --no-deps brain sh -c "go build -o /tmp/upmo ./cmd/upmo && /tmp/upmo $1" >"$out"; then
    cat "$out" >&2
    rm -f "$out"
    return 1
  fi
  awk '/^http/{url=$0} END{print url}' "$out"
  rm -f "$out"
}

say "issuing a bootstrap link"
link=$(issue "bootstrap --email ada@example.com --name Ada" || true)
case $link in
  http*) ;;
  *)
    say "bootstrap refused; issuing a recover link"
    link=$(issue "recover --email ada@example.com")
    ;;
esac
case $link in
  http*) ;;
  *)
    say "brain did not print a link"
    exit 1
    ;;
esac

# The gateway answers the chat replies of e2e/composer.mjs.
say "starting gateway and brain"
dc up -d --wait gateway brain

# Last, so the throttle is empty when the browser starts.
case $project in
  upmo-e2e-*)
    say "resetting the e2e person and the throttle"
    dc exec -T postgres psql -U upmo -d upmo -q -v ON_ERROR_STOP=1 -f - <e2e/reset.sql >&2
    say "seeding the e2e tasks"
    dc exec -T postgres psql -U upmo -d upmo -q -v ON_ERROR_STOP=1 -f - <e2e/seed-tasks.sql >&2
    ;;
esac
printf '%s\n' "$link"

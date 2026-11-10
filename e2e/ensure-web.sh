#!/bin/sh
# Print two lines on fd 3: the compose project and the URL Playwright should
# open. Logs go to stderr.
# e2e always runs in its own project, upmo-e2e-<hash of this checkout path>,
# never in the default (dev) project, so a running dev stack and its database
# are not touched. A web container of that project whose /src is this checkout
# is reused.
set -eu

cd "$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
root=$(pwd -P)

if [ -z "${UPMO_GIT_DIR:-}" ]; then
  UPMO_GIT_DIR=$(sed -n 's|^gitdir: \(.*\)/worktrees/[^/]*$|\1|p' .git 2>/dev/null || true)
  export UPMO_GIT_DIR
fi

if ! { true >&3; } 2>/dev/null; then
  exec 3>&1
fi

say() {
  printf '%s\n' "$*" >&2
}

norm() {
  if [ -n "$1" ] && [ -d "$1" ]; then
    (CDPATH= cd -- "$1" && pwd -P)
  else
    printf '%s\n' "$1"
  fi
}

# Host path of the mount whose JSON object contains "/src".
src_of() {
  json=$(docker inspect "$1" --format '{{json .Mounts}}') || return 1
  printf '%s\n' "$json" | awk '
    function source_of(obj,    key, pos, i, c, out) {
      key = "\"Source\":\""
      pos = index(obj, key)
      if (pos == 0) {
        return ""
      }
      i = pos + length(key)
      out = ""
      while (i <= length(obj)) {
        c = substr(obj, i, 1)
        if (c == "\\") {
          out = out substr(obj, i + 1, 1)
          i += 2
          continue
        }
        if (c == "\"") {
          break
        }
        out = out c
        i++
      }
      return out
    }
    {
      n = split($0, parts, /[{}]/)
      for (i = 1; i <= n; i++) {
        if (index(parts[i], "\"/src\"") == 0) {
          continue
        }
        print source_of(parts[i])
        exit
      }
    }
  '
}

dc_args=
dc() {
  # shellcheck disable=SC2086
  docker compose $dc_args "$@"
}

vite_up() {
  dc exec -T web node -e "fetch('http://127.0.0.1:5173/').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))" >/dev/null 2>&1
}

wait_ready() {
  i=0
  while ! vite_up; do
    i=$((i + 1))
    if [ "$i" -ge 180 ]; then
      say "web dev server did not become ready"
      dc logs --tail 80 web >&2 || true
      exit 1
    fi
    sleep 1
  done
}

finish() {
  printf '%s\n' "$1" >&3
  printf '%s\n' "http://localhost:5173" >&3
  exit 0
}

start_isolated() {
  hash=$(printf '%s' "$root" | cksum | awk '{print $1}')
  project="upmo-e2e-$hash"
  dc_args="-p $project -f compose.yml -f e2e/web.override.yml"
  iso_id=$(dc ps -q web || true)
  if [ -n "$iso_id" ] && [ "$(norm "$(src_of "$iso_id")")" = "$root" ] && vite_up; then
    say "web dev server already up"
    finish "$project"
  fi
  say "starting web dev server for this checkout"
  if [ -z "$iso_id" ]; then
    dc run --rm --no-deps web pnpm install --frozen-lockfile
  fi
  dc up -d --no-deps web
  wait_ready
  finish "$project"
}

start_isolated

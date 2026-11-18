#!/bin/sh
# Run the e2e against this checkout in its own e2e project, never the default
# dev stack (its database holds a real person). The project is named from the
# real path of the checkout, so a worktree always gets its own stack, and a
# symlink to it gets the same one as ensure-web.sh and `make e2e` use.
# usage: isolated.sh              run the whole suite (e2e/run.sh)
#        isolated.sh a.mjs b.mjs  run passkeys first, then just these scripts
# E2E_WEB=preview serves the production build (vite preview) instead of the dev
# server and runs the checks under the production Content-Security-Policy.
set -eu

cd "$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)"
project="upmo-e2e-$(printf '%s' "$(pwd -P)" | cksum | awk '{print $1}')"

if [ -z "${UPMO_GIT_DIR:-}" ]; then
  UPMO_GIT_DIR=$(sed -n 's|^gitdir: \(.*\)/worktrees/[^/]*$|\1|p' .git 2>/dev/null || true)
  export UPMO_GIT_DIR
fi

files="-f compose.yml -f e2e/web.override.yml"
csp=
if [ "${E2E_WEB:-}" = "preview" ]; then
  files="$files -f e2e/web.preview.yml"
  csp="production"
fi

dc() {
  # shellcheck disable=SC2086
  docker compose -p "$project" $files "$@"
}

web_up() {
  dc exec -T web node -e "fetch('http://127.0.0.1:5173/').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))" >/dev/null 2>&1
}

# Restart the brain so it runs the code in this checkout (and its migrations).
dc restart brain >/dev/null 2>&1 || true
link=$(sh e2e/ensure-brain.sh "$project") || exit 1

dc run --rm --no-deps web pnpm install --frozen-lockfile >&2
if [ -n "$csp" ]; then
  dc up -d --no-deps --force-recreate web >&2
else
  dc up -d --no-deps web >&2
fi
i=0
while ! web_up; do
  i=$((i + 1))
  if [ "$i" -ge 120 ]; then
    printf '%s\n' "web server did not answer on :5173 within 120 s" >&2
    dc logs --tail 80 web >&2 || true
    exit 1
  fi
  sleep 1
done

if [ "$#" -eq 0 ]; then
  cmd='sh /src/e2e/run.sh'
else
  cmd='sh /src/e2e/install.sh && node /src/e2e/passkeys.mjs'
  for s in "$@"; do cmd="$cmd && node /src/e2e/$s"; done
fi
if [ -n "$csp" ]; then
  dc --profile e2e run --rm -e WEB_URL=http://localhost:5173 -e UPMO_LINK="$link" -e CSP_MODE="$csp" playwright sh -c "$cmd"
else
  dc --profile e2e run --rm -e WEB_URL=http://localhost:5173 -e UPMO_LINK="$link" playwright sh -c "$cmd"
fi

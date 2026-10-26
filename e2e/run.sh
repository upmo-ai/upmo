#!/bin/sh
# Install Playwright into a volume, then run the browser checks.
# The image tag and this version stay on the same 1.61.1 release so the
# baked browsers match the library.
set -eu

version=1.61.1
prefix=/opt/playwright
pkg="$prefix/node_modules/playwright/package.json"

installed=""
if [ -f "$pkg" ]; then
  installed=$(node -p "require('$pkg').version")
fi

if [ "$installed" != "$version" ]; then
  mkdir -p "$prefix"
  if [ ! -f "$prefix/package.json" ]; then
    printf '%s\n' '{"name":"upmo-e2e","private":true}' > "$prefix/package.json"
  fi
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install \
    --prefix "$prefix" \
    --save-exact \
    --no-fund \
    --no-audit \
    "playwright@$version"
fi

# Passkeys first: it consumes the bootstrap link and sets the password the
# other checks sign in with.
node /src/e2e/passkeys.mjs
node /src/e2e/theme.mjs
node /src/e2e/shell.mjs
node /src/e2e/chat.mjs
node /src/e2e/usage.mjs
node /src/e2e/flows.mjs
exec node /src/e2e/identity.mjs

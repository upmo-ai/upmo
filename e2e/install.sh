#!/bin/sh
# Install Playwright into the /opt/playwright volume when it is missing or
# another version. The image tag and this version stay on the same 1.61.1
# release so the baked browsers match the library.
set -eu

version=1.61.1
prefix=/opt/playwright
pkg="$prefix/node_modules/playwright/package.json"

installed=""
if [ -f "$pkg" ]; then
  installed=$(node -p "require('$pkg').version")
fi

# pg reads the ledger for e2e/composer.mjs, the one fact no API shows.
if [ "$installed" != "$version" ] || [ ! -d "$prefix/node_modules/pg" ]; then
  mkdir -p "$prefix"
  if [ ! -f "$prefix/package.json" ]; then
    printf '%s\n' '{"name":"upmo-e2e","private":true}' > "$prefix/package.json"
  fi
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install \
    --prefix "$prefix" \
    --save-exact \
    --no-fund \
    --no-audit \
    "playwright@$version" \
    pg@8.16.3
fi

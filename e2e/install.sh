#!/bin/sh
# Install Playwright into the /opt/playwright volume when it is missing or
# another version. The image tag and this version stay on the same 1.65.1
# release so the baked browsers match the library.
set -eu

version=1.65.1
prefix=/opt/playwright
pkg="$prefix/node_modules/playwright/package.json"

installed=""
if [ -f "$pkg" ]; then
  installed=$(node -p "require('$pkg').version")
fi

# pg reads the ledger for e2e/composer.mjs, the one fact no API shows. The
# versions and hashes come from e2e/package-lock.json. The stamp is the
# lockfile of the last install that finished, so a broken one runs again.
stamp="$prefix/.installed-lock"
if [ "$installed" != "$version" ] || [ ! -d "$prefix/node_modules/pg" ] ||
  ! cmp -s /src/e2e/package-lock.json "$stamp"; then
  mkdir -p "$prefix"
  rm -f "$stamp"
  cp /src/e2e/package.json /src/e2e/package-lock.json "$prefix/"
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci \
    --prefix "$prefix" \
    --no-fund \
    --no-audit
  cp /src/e2e/package-lock.json "$stamp"
fi

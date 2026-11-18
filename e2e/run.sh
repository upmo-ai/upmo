#!/bin/sh
# Install Playwright into a volume, then run the browser checks.
set -eu

sh /src/e2e/install.sh

# Passkeys first: it consumes the bootstrap link and sets the password the
# other checks sign in with.
node /src/e2e/passkeys.mjs
node /src/e2e/theme.mjs
node /src/e2e/brand.mjs
node /src/e2e/shell.mjs
node /src/e2e/chat.mjs
node /src/e2e/bell.mjs
node /src/e2e/usage.mjs
node /src/e2e/connectors.mjs
node /src/e2e/providers.mjs
node /src/e2e/models.mjs
node /src/e2e/flows.mjs
node /src/e2e/stream.mjs
node /src/e2e/composer.mjs
node /src/e2e/memory.mjs
node /src/e2e/knowledge.mjs
node /src/e2e/artifacts.mjs
exec node /src/e2e/identity.mjs

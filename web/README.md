# Upmo web

React app on Mantine 9, built with Vite and tested with Vitest. Run it through Docker from the repository root, for example `make web` or `docker compose run --rm --service-ports web pnpm dev`.

Code is grouped by feature in step with the bounded contexts of the Go module: everything for one context lives under `src/features/<context>/`.

## Brand kit

The favicon, app icons, web manifest and share image come from `@upmo-ai/brand`. They are not committed. `plugins/brand-kit.ts` serves them from the site root in dev and writes them into `dist/` in build, and it replaces the `<!-- brand-kit -->` line in `index.html` with the kit's head tags (icons, manifest, theme colour).

The `og:image` tags need an absolute URL, so they are added only when `VITE_SITE_ORIGIN` is set to the site origin at build time. Through Docker, set it in the shell that runs make: `VITE_SITE_ORIGIN=https://upmo.example make build`. The `web` service in `compose.yml` passes it into the container. Outside Docker, set it for the command: `VITE_SITE_ORIGIN=https://upmo.example pnpm build`. Vite also reads it from a `.env` file in `web/`.

When it is unset or empty the tags are left out. A value that is not an absolute `http` or `https` origin with a plain host (letters, digits, dots and hyphens, or an IPv6 literal, with an optional port) fails the build and the message names `VITE_SITE_ORIGIN`.

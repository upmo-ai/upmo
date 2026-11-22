# Upmo web

React app on Mantine 9, built with Vite and tested with Vitest. Run it through Docker from the repository root. For example: `make web` or `docker compose run --rm --service-ports web pnpm dev`.

Code is grouped by feature, matching the bounded contexts of the Go module. Everything for one context lives under `src/features/<context>/`.

## Brand kit

The favicon, app icons, web manifest and share image come from `@upmo-ai/brand`. They are not committed.

`plugins/brand-kit.ts` does 3 things:

- In dev, it serves the files from the site root.
- In build, it writes them into `dist/`.
- It replaces the `<!-- brand-kit -->` line in `index.html` with the kit's head tags (icons, manifest, theme color).

The `og:image` tags need an absolute URL. They are added only when `VITE_SITE_ORIGIN` is set to the site origin at build time.

With Docker, set it in the shell that runs make: `VITE_SITE_ORIGIN=https://upmo.example make build`. The `web` service in `compose.yml` passes it into the container.

Without Docker, set it for the command: `VITE_SITE_ORIGIN=https://upmo.example pnpm build`. Vite also reads it from a `.env` file in `web/`.

If it is unset or empty, the tags are left out. The build fails if the value is not an absolute `http` or `https` origin with a plain host. A plain host is letters, digits, dots and hyphens, or an IPv6 literal, with an optional port. The error message names `VITE_SITE_ORIGIN`.

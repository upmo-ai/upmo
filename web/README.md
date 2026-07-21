# Upmo web

React app on Mantine 9, built with Vite and tested with Vitest. Run it through Docker from the repository root, for example `make web` or `docker compose run --rm --service-ports web pnpm dev`.

Code is grouped by feature in step with the bounded contexts of the Go module: everything for one context lives under `src/features/<context>/`.

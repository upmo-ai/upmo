import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

function liveFetch() {
  return (async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/api/session")) return Response.json({ user_id: "ada", has_password: true });
    if (url.endsWith("/api/passkeys")) return Response.json([{ id: "passkey" }]);
    if (url.endsWith("/api/teams")) return Response.json([{ id: "team-finance", name: "Finance" }]);
    if (url.endsWith("/api/roles")) {
      return Response.json([
        {
          id: "role-owner",
          name: "Owner",
          system: "owner",
          permissions: ["members:manage", "teams:manage", "roles:manage", "task:view"],
          members: 1,
        },
      ]);
    }
    if (url.endsWith("/api/members")) {
      return Response.json([
        {
          id: "ada",
          name: "Ada",
          email: "ada@example.com",
          invited: false,
          teams: [],
          role: { id: "role-owner", name: "Owner", system: "owner" },
        },
        {
          id: "bob",
          name: "Bob",
          email: "bob@example.com",
          invited: false,
          teams: [],
          role: { id: "role-owner", name: "Owner", system: "owner" },
        },
      ]);
    }
    if (url.endsWith("/api/tokens")) {
      return Response.json([
        {
          id: "tok-1",
          name: "Ship",
          scope: ["task:view"],
          created_at: "2026-10-01T00:00:00Z",
          expires_at: "2026-12-30T00:00:00Z",
          last_used_at: null,
        },
      ]);
    }
    return new Response("missing", { status: 404 });
  }) as typeof fetch;
}

type Page = { name: string; path: string; title: string; live?: boolean; settled: () => Promise<unknown> };

const pages: Page[] = [
  { name: "members (stub)", path: "/admin/members", title: "Members", settled: () => screen.findByRole("table", { name: "Members" }) },
  {
    name: "members (live)",
    path: "/admin/members",
    title: "Members",
    live: true,
    settled: () => screen.findByText("bob@example.com"),
  },
  { name: "teams", path: "/admin/teams", title: "Teams", settled: () => screen.findByRole("table", { name: "Teams" }) },
  { name: "roles", path: "/admin/roles", title: "Roles", settled: () => screen.findByRole("table", { name: "Roles" }) },
  { name: "tokens (stub)", path: "/tokens", title: "API tokens", settled: () => screen.findByRole("button", { name: "Create token" }) },
  { name: "tokens (live)", path: "/tokens", title: "API tokens", live: true, settled: () => screen.findByText("Ship") },
  { name: "security", path: "/security", title: "Security", settled: () => screen.findByRole("heading", { level: 2, name: "Passkeys" }) },
];

describe("identity page headings", () => {
  const original = globalThis.fetch;

  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    globalThis.fetch = original;
    cleanup();
  });

  it.each(pages)("$name exposes exactly one level-1 heading", async ({ path, title, live, settled }) => {
    if (live) globalThis.fetch = liveFetch();
    renderAt(path);
    await screen.findByRole("heading", { level: 1, name: title });
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);

    await settled();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });
});

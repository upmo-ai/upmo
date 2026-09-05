import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";
import { sampleFacts, sampleProfile } from "./sample";

type Row = { id: string; claim: string; trust: string; confirmed_at: string };

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

describe("Personalisation page", () => {
  const calls: { method: string; url: string; body: string }[] = [];
  let page: { profile: string; facts: Row[] };
  const original = globalThis.fetch;

  beforeEach(() => {
    localStorage.clear();
    calls.length = 0;
    page = {
      profile: sampleProfile,
      facts: sampleFacts.map((fact) => ({
        id: fact.id,
        claim: fact.text,
        trust: "user",
        confirmed_at: "2026-10-31T02:00:00Z",
      })),
    };
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? init.body : "";
      if (!url.includes("/api/personalization")) return original(input, init);
      calls.push({ method, url, body });
      if (method === "PUT" && url.endsWith("/profile")) {
        page = { ...page, profile: (JSON.parse(body) as { body: string }).body };
        return Response.json({ body: page.profile });
      }
      if (method === "POST" && url.endsWith("/facts")) {
        const claim = (JSON.parse(body) as { claim: string }).claim;
        page = {
          ...page,
          facts: [{ id: "fact-new", claim, trust: "user", confirmed_at: "2026-10-31T02:00:00Z" }, ...page.facts],
        };
        return Response.json({ id: "fact-new" }, { status: 201 });
      }
      if (method === "PUT" && url.includes("/facts/")) {
        const id = url.split("/").at(-1);
        const claim = (JSON.parse(body) as { claim: string }).claim;
        page = {
          ...page,
          facts: page.facts.map((fact) => (fact.id === id ? { ...fact, id: "fact-edited", claim } : fact)),
        };
        return Response.json({ id: "fact-edited" });
      }
      if (method === "DELETE" && url.includes("/facts/")) {
        const id = url.split("/").at(-1);
        page = { ...page, facts: page.facts.filter((fact) => fact.id !== id) };
        return new Response(null, { status: 204 });
      }
      return Response.json({ profile: page.profile, facts: page.facts.map((fact) => ({ ...fact })) });
    }) as typeof fetch;
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = original;
  });

  it("shows the profile and sample facts under Settings", async () => {
    renderAt("/settings/personalization");
    expect(await screen.findByRole("heading", { level: 1, name: "Personalisation" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Profile" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Memory" })).toBeTruthy();
    expect(await screen.findByText(sampleProfile)).toBeTruthy();
    expect(screen.getByText(sampleFacts[0]!.text)).toBeTruthy();
    expect(screen.getAllByText(/Added by you/).length).toBe(sampleFacts.length);
    expect(
      screen.getByText("What Upmo remembers about you. Facts stay personal. Documents belong in Knowledge."),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Promote" })).toBeNull();
    expect(screen.queryByText(/^Team /)).toBeNull();
    expect(screen.queryByText(/^Agent /)).toBeNull();
    const settings = screen.getByRole("navigation", { name: "Settings" });
    expect(within(settings).getByRole("link", { name: "Personalisation" }).getAttribute("aria-current")).toBe(
      "page",
    );
  });

  it("adds, edits, and forgets a fact", async () => {
    renderAt("/settings/personalization");
    await screen.findByRole("heading", { level: 1, name: "Personalisation" });

    await screen.findByText(sampleFacts[0]!.text);
    fireEvent.change(screen.getByLabelText("Add a fact"), { target: { value: "Prefers tables over prose." } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("Prefers tables over prose.")).toBeTruthy();
    expect(calls.some((call) => call.method === "POST" && call.body.includes("Prefers tables over prose."))).toBe(true);

    const first = screen.getByText(sampleFacts[0]!.text).closest(".memory-row");
    expect(first).toBeTruthy();
    act(() => {
      within(first as HTMLElement).getByRole("button", { name: "Edit" }).click();
    });
    fireEvent.change(screen.getByLabelText("Fact"), { target: { value: "Prefers British spelling in every reply." } });
    act(() => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    expect(await screen.findByText("Prefers British spelling in every reply.")).toBeTruthy();
    expect(calls.some((call) => call.method === "PUT" && call.url.endsWith("/facts/fact-spelling"))).toBe(true);

    const edited = screen.getByText("Prefers British spelling in every reply.").closest(".memory-row");
    act(() => {
      within(edited as HTMLElement).getByRole("button", { name: "Forget" }).click();
    });
    expect(await screen.findByRole("dialog", { name: "Forget this fact?" })).toBeTruthy();
    act(() => {
      within(screen.getByRole("dialog")).getByRole("button", { name: "Forget" }).click();
    });
    expect(await screen.findByText(sampleFacts[1]!.text)).toBeTruthy();
    expect(screen.queryByText("Prefers British spelling in every reply.")).toBeNull();
    expect(calls.some((call) => call.method === "DELETE" && call.url.endsWith("/facts/fact-edited"))).toBe(true);
  });

  it("saves an edited profile", async () => {
    renderAt("/settings/personalization");
    await screen.findByText(sampleProfile);
    act(() => {
      screen.getAllByRole("button", { name: "Edit" })[0]!.click();
    });
    fireEvent.change(screen.getByLabelText("Profile"), {
      target: { value: "Prefers short answers and tables." },
    });
    act(() => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    expect(await screen.findByText("Prefers short answers and tables.")).toBeTruthy();
    expect(calls.some((call) => call.method === "PUT" && call.url.endsWith("/profile"))).toBe(true);
  });
});

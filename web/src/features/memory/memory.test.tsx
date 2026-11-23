import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { BrainError, createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";
import { sampleFacts, sampleProfile } from "./sample";

type Row = { id: string; claim: string; trust: string; confirmed_at: string };

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
}

describe("Personalisation page", () => {
  const calls: { method: string; url: string; body: string }[] = [];
  let page: { profile: string; facts: Row[] };
  let loadError: string | null;
  let saveError: { status: number; message: string } | null;
  const original = globalThis.fetch;

  beforeEach(() => {
    localStorage.clear();
    calls.length = 0;
    loadError = null;
    saveError = null;
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
      if (method === "PUT" && url.includes("/facts/") && saveError !== null) {
        return Response.json({ error: saveError.message }, { status: saveError.status });
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
      if (loadError !== null) return Response.json({ error: loadError }, { status: 500 });
      return Response.json({ profile: page.profile, facts: page.facts.map((fact) => ({ ...fact })) });
    }) as typeof fetch;
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = original;
  });

  it("shows the profile and sample facts under Personalisation", async () => {
    renderAt("/personalisation");
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
    expect(screen.queryByRole("navigation", { name: "Settings" })).toBeNull();
  });

  it("adds, edits, and forgets a fact", async () => {
    renderAt("/personalisation");
    await screen.findByRole("heading", { level: 1, name: "Personalisation" });

    await screen.findByText(sampleFacts[0]!.text);
    fireEvent.change(screen.getByLabelText("Add a fact"), { target: { value: "Prefers tables over prose." } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("Prefers tables over prose.")).toBeTruthy();
    expect(calls.some((call) => call.method === "POST" && call.body.includes("Prefers tables over prose."))).toBe(true);

    const first = screen.getByRole("row", { name: new RegExp(sampleFacts[0]!.text) });
    act(() => {
      within(first).getByRole("button", { name: "Edit fact" }).click();
    });
    fireEvent.change(screen.getByLabelText("Fact"), { target: { value: "Prefers British spelling in every reply." } });
    act(() => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    expect(await screen.findByText("Prefers British spelling in every reply.")).toBeTruthy();
    expect(calls.some((call) => call.method === "PUT" && call.url.endsWith("/facts/fact-spelling"))).toBe(true);
    await waitFor(() => expect(screen.queryByLabelText("Fact")).toBeNull());
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    expect(
      within(screen.getByRole("row", { name: /Prefers British spelling in every reply\./ })).getByRole("button", {
        name: "Edit fact",
      }),
    ).toBeTruthy();

    const edited = screen.getByRole("row", { name: /Prefers British spelling in every reply\./ });
    act(() => {
      within(edited).getByRole("button", { name: "Forget fact" }).click();
    });
    expect(await screen.findByRole("dialog", { name: "Forget this fact?" })).toBeTruthy();
    act(() => {
      within(screen.getByRole("dialog")).getByRole("button", { name: "Forget fact" }).click();
    });
    await waitFor(() => expect(screen.queryByText("Prefers British spelling in every reply.")).toBeNull());
    expect(screen.queryByRole("row", { name: /Prefers British spelling in every reply\./ })).toBeNull();
    expect(screen.getByText(sampleFacts[1]!.text)).toBeTruthy();
    expect(calls.some((call) => call.method === "DELETE" && call.url.endsWith("/facts/fact-edited"))).toBe(true);
  });

  it("lists facts in a table with a source and an accessible actions column", async () => {
    renderAt("/personalisation");
    await screen.findByText(sampleFacts[0]!.text);
    const table = screen.getByRole("table");
    expect(within(table).getByRole("columnheader", { name: "Fact" })).toBeTruthy();
    expect(within(table).getByRole("columnheader", { name: "Source" })).toBeTruthy();
    expect(within(table).getByRole("columnheader", { name: "Actions" })).toBeTruthy();
    const row = within(table).getByRole("row", { name: new RegExp(sampleFacts[0]!.text) });
    expect(within(row).getByText("Added by you · 31 Oct 2026")).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Edit fact" })).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Forget fact" })).toBeTruthy();
    expect(screen.getAllByRole("heading", { level: 1 }).length).toBe(1);
  });

  it("UPM-196 names the facts table and the region that scrolls it", async () => {
    renderAt("/personalisation");
    await screen.findByText(sampleFacts[0]!.text);
    expect(screen.getByRole("table", { name: "Remembered facts" })).toBeTruthy();
    const region = screen.getByRole("region", { name: "Remembered facts" });
    expect(region.getAttribute("tabindex")).toBe("0");
    expect(within(region).getByRole("table")).toBeTruthy();
  });

  it("keeps the facts table at least 560px wide, scrolling the region instead of squeezing it", async () => {
    renderAt("/personalisation");
    await screen.findByText(sampleFacts[0]!.text);
    const region = screen.getByRole("region", { name: "Remembered facts" });
    // Mantine writes the minimum as a rem value: 560px is 35rem.
    expect(region.style.getPropertyValue("--table-min-width")).toContain("35rem");
  });

  it("shows the empty state outside a table when nothing is remembered", async () => {
    page = { ...page, facts: [] };
    renderAt("/personalisation");
    expect(await screen.findByRole("heading", { level: 3, name: "Nothing remembered yet" })).toBeTruthy();
    expect(
      screen.getByText("Upmo keeps facts you tell it and lessons from tasks. You can delete any of them."),
    ).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a critical alert when the page cannot load", async () => {
    loadError = "Personalisation is unavailable";
    renderAt("/personalisation");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Personalisation is unavailable");
    expect(alert.getAttribute("data-tone")).toBe("critical");
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByRole("heading", { level: 3, name: "Nothing remembered yet" })).toBeNull();
    const edit = screen.getByRole("button", { name: "Edit profile" });
    expect(edit.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(edit);
    expect(screen.queryByLabelText("Profile")).toBeNull();
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
  });

  it("keeps a fact in edit mode with the error when saving fails", async () => {
    saveError = { status: 409, message: "That fact is already remembered" };
    renderAt("/personalisation");
    const text = sampleFacts[0]!.text;
    await screen.findByText(text);
    act(() => {
      within(screen.getByRole("row", { name: new RegExp(text) })).getByRole("button", { name: "Edit fact" }).click();
    });
    fireEvent.change(screen.getByLabelText("Fact"), { target: { value: sampleFacts[1]!.text } });
    act(() => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("That fact is already remembered");
    expect((screen.getByLabelText("Fact") as HTMLTextAreaElement).value).toBe(sampleFacts[1]!.text);
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy();
    expect(calls.some((call) => call.method === "PUT" && call.url.endsWith(`/facts/${sampleFacts[0]!.id}`))).toBe(true);
  });

  it("restores the row when an edit is canceled", async () => {
    renderAt("/personalisation");
    const text = sampleFacts[0]!.text;
    await screen.findByText(text);
    act(() => {
      within(screen.getByRole("row", { name: new RegExp(text) })).getByRole("button", { name: "Edit fact" }).click();
    });
    const field = screen.getByLabelText("Fact") as HTMLTextAreaElement;
    expect(field.value).toBe(text);
    fireEvent.change(field, { target: { value: "Something else entirely." } });
    act(() => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    expect(screen.queryByLabelText("Fact")).toBeNull();
    const row = screen.getByRole("row", { name: new RegExp(text) });
    expect(within(row).getByText(text)).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Edit fact" })).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Forget fact" })).toBeTruthy();
    expect(screen.queryByText("Something else entirely.")).toBeNull();
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
  });

  it("keeps the fact when forgetting is canceled", async () => {
    renderAt("/personalisation");
    const text = sampleFacts[0]!.text;
    await screen.findByText(text);
    act(() => {
      within(screen.getByRole("row", { name: new RegExp(text) })).getByRole("button", { name: "Forget fact" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "Forget this fact?" });
    act(() => {
      within(dialog).getByRole("button", { name: "Cancel" }).click();
    });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Forget this fact?" })).toBeNull());
    expect(screen.getByRole("row", { name: new RegExp(text) })).toBeTruthy();
    expect(calls.some((call) => call.method === "DELETE")).toBe(false);
  });

  it("saves an edited profile", async () => {
    renderAt("/personalisation");
    await screen.findByText(sampleProfile);
    act(() => {
      screen.getByRole("button", { name: "Edit profile" }).click();
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

  it("UPM-415 uploads and removes a profile avatar and updates the shell avatar", async () => {
    renderAt("/personalisation", createStubApi());
    const card = await screen.findByTestId("profile-avatar-card");
    expect(within(card).getByRole("button", { name: "Upload photo" })).toBeTruthy();
    expect(within(card).queryByRole("button", { name: "Remove photo" })).toBeNull();
    expect(within(card).getByText("PNG, JPEG, GIF, or WebP. Up to 50 MB.")).toBeTruthy();

    const input = within(card).getByLabelText("Upload avatar") as HTMLInputElement;
    const png = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "me.png", { type: "image/png" });
    fireEvent.change(input, { target: { files: [png] } });

    const preview = (await within(card).findByRole("img", { name: "Your avatar" })) as HTMLImageElement;
    expect(preview.getAttribute("src")).toContain("/api/users/sam/avatar?v=");
    expect(within(card).getByRole("button", { name: "Change photo" })).toBeTruthy();
    expect(within(card).getByRole("button", { name: "Remove photo" })).toBeTruthy();

    await waitFor(() => {
      const shellImg = document.querySelector(".shell-avatar-img") as HTMLImageElement | null;
      expect(shellImg?.getAttribute("src")).toBe(preview.getAttribute("src"));
    });

    fireEvent.click(within(card).getByRole("button", { name: "Remove photo" }));
    await waitFor(() => {
      expect(within(card).queryByRole("img", { name: "Your avatar" })).toBeNull();
    });
    expect(within(card).getByRole("button", { name: "Upload photo" })).toBeTruthy();
    expect(within(card).queryByRole("button", { name: "Remove photo" })).toBeNull();
    await waitFor(() => {
      expect(document.querySelector(".shell-avatar-img")).toBeNull();
    });
  });

  it("UPM-415 rejects unsupported types and oversized files client-side and maps server 413/415 alerts", async () => {
    let uploadCalls = 0;
    let serverErr: BrainError | null = null;
    const base = createStubApi();
    const client: ApiClient = {
      ...base,
      me: {
        ...base.me,
        async uploadAvatar(file) {
          uploadCalls += 1;
          if (serverErr) throw serverErr;
          return base.me.uploadAvatar(file);
        },
      },
    };
    renderAt("/personalisation", client);
    const card = await screen.findByTestId("profile-avatar-card");
    const input = within(card).getByLabelText("Upload avatar") as HTMLInputElement;

    const svg = new File(["<svg/>"], "bad.svg", { type: "image/svg+xml" });
    fireEvent.change(input, { target: { files: [svg] } });
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Avatar must be a PNG, JPEG, GIF, or WebP image.",
    );
    expect(uploadCalls).toBe(0);

    const big = new File(["x"], "huge.png", { type: "image/png" });
    Object.defineProperty(big, "size", { value: 50 * 1024 * 1024 + 1 });
    fireEvent.change(input, { target: { files: [big] } });
    expect((await screen.findByRole("alert")).textContent).toContain("Avatar must be 50 MB or smaller.");
    expect(uploadCalls).toBe(0);

    serverErr = new BrainError(413, "too large");
    const okPng = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "ok.png", { type: "image/png" });
    fireEvent.change(input, { target: { files: [okPng] } });
    await waitFor(() => expect(uploadCalls).toBe(1));
    expect((await screen.findByRole("alert")).textContent).toContain("Avatar must be 50 MB or smaller.");

    serverErr = new BrainError(415, "unsupported");
    fireEvent.change(input, { target: { files: [okPng] } });
    await waitFor(() => expect(uploadCalls).toBe(2));
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Avatar must be a PNG, JPEG, GIF, or WebP image.",
    );
  });
});

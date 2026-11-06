import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createChatsStub } from "./stub";

describe("UPM-201 chat stub", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("UPM-201 stores the line at once and writes the reply later", async () => {
    const chats = createChatsStub(500);
    const created = await chats.create();

    await chats.send(created.id, "Hello there");
    expect((await chats.get(created.id)).lines.map((line) => line.role)).toEqual(["user"]);

    await vi.advanceTimersByTimeAsync(500);
    const lines = (await chats.get(created.id)).lines;
    expect(lines.map((line) => line.role)).toEqual(["user", "assistant"]);
    expect(lines[1]?.model).toBeTruthy();
    expect(lines[1]?.cost_usd).toBeGreaterThan(0);
  });

  it("UPM-201 refuses a second line while a reply is awaited, and an empty line", async () => {
    const chats = createChatsStub(500);
    const created = await chats.create();

    await expect(chats.send(created.id, "   ")).rejects.toThrow("Write a message.");
    await chats.send(created.id, "One");
    await expect(chats.send(created.id, "Two")).rejects.toThrow("Wait for the reply.");
  });

  it("UPM-201 lists the newest activity first and previews the newest line", async () => {
    const chats = createChatsStub(0);
    const before = await chats.list();
    const quiet = before.at(-1);
    if (!quiet) throw new Error("no fixtures");

    await chats.send(quiet.id, "Bring this one to the top");
    const after = await chats.list();

    expect(after[0]?.id).toBe(quiet.id);
    expect(after[0]?.preview).toBe("Bring this one to the top");
  });

  it("UPM-201 hands out copies, so a caller cannot change a stored chat", async () => {
    const chats = createChatsStub(0);
    const [first] = await chats.list();
    if (!first) throw new Error("no fixtures");

    const read = await chats.get(first.id);
    read.lines.length = 0;

    expect((await chats.get(first.id)).lines.length).toBeGreaterThan(0);
  });

  it("UPM-201 refuses a chat that does not exist", async () => {
    const chats = createChatsStub(0);

    await expect(chats.get("nope")).rejects.toThrow("Chat missing.");
    await expect(chats.send("nope", "Hello")).rejects.toThrow("Chat missing.");
  });
});

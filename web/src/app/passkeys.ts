// Browser half of the passkey ceremonies. The server keeps the challenge.

interface JSONCredential extends Credential {
  toJSON(): unknown;
}

function credentialJSON(credential: Credential): unknown {
  if (typeof (credential as Partial<JSONCredential>).toJSON !== "function") {
    throw new Error("unsupported");
  }
  return (credential as JSONCredential).toJSON();
}

// The autofill ceremony that has not ended yet. `done` settles when its options
// request, prompt and finish have all stopped, even after an abort.
let conditional: { controller: AbortController; done: Promise<void> } | null = null;

export function abortConditional(): void {
  conditional?.controller.abort();
}

// True while an autofill ceremony is waiting on the person and has not been aborted.
export function conditionalPending(): boolean {
  return conditional !== null && !conditional.controller.signal.aborted;
}

// Aborts the autofill ceremony and waits for it to stop. The server keeps one
// challenge per browser, so a new options request must not start while the
// last one is still in flight.
async function endConditional(): Promise<void> {
  while (conditional) {
    const running = conditional;
    running.controller.abort();
    await running.done;
    if (conditional === running) {
      conditional = null;
    }
  }
}

type PublicKeyOptions = { publicKey: PublicKeyCredentialCreationOptionsJSON | PublicKeyCredentialRequestOptionsJSON };

function creationOptions(options: PublicKeyOptions): PublicKeyCredentialCreationOptions {
  const parse = window.PublicKeyCredential?.parseCreationOptionsFromJSON;
  if (typeof parse !== "function") {
    throw new Error("unsupported");
  }
  return parse.call(window.PublicKeyCredential, options.publicKey as PublicKeyCredentialCreationOptionsJSON);
}

function requestOptions(options: PublicKeyOptions): PublicKeyCredentialRequestOptions {
  const parse = window.PublicKeyCredential?.parseRequestOptionsFromJSON;
  if (typeof parse !== "function") {
    throw new Error("unsupported");
  }
  return parse.call(window.PublicKeyCredential, options.publicKey as PublicKeyCredentialRequestOptionsJSON);
}

function creationUserId(options: PublicKeyOptions): string | undefined {
  const user = (options.publicKey as { user?: { id?: unknown } }).user;
  return typeof user?.id === "string" && user.id !== "" ? user.id : undefined;
}

export async function signInWithPasskey(): Promise<void> {
  await endConditional();
  const options = await postJSON<PublicKeyOptions>("/api/passkeys/sign-in/options", {});
  const credential = await navigator.credentials.get({ publicKey: requestOptions(options) });
  if (!credential) {
    throw new Error("cancelled");
  }
  await postRaw("/api/passkeys/sign-in/finish", credentialJSON(credential));
}

export async function createPasskeyFromLink(token: string): Promise<void> {
  const options = await postJSON<PublicKeyOptions>("/api/passkeys/link/options", { token });
  const credential = await navigator.credentials.create({ publicKey: creationOptions(options) });
  if (!credential) {
    throw new Error("unsupported");
  }
  await postRaw("/api/passkeys/link/finish", { token, response: credentialJSON(credential) });
}

export async function conditionalPasskey(current: () => boolean = () => true): Promise<boolean> {
  // Another call may start while this one waits, so look again before claiming the slot.
  do {
    await endConditional();
  } while (conditional);
  const controller = new AbortController();
  const running = runConditional(controller, current);
  const entry = { controller, done: running.then(noop, noop) };
  conditional = entry;
  try {
    return await running;
  } finally {
    if (conditional === entry) {
      conditional = null;
    }
  }
}

function noop(): void {}

async function runConditional(controller: AbortController, current: () => boolean): Promise<boolean> {
  const available = window.PublicKeyCredential?.isConditionalMediationAvailable;
  if (!available || !(await available.call(window.PublicKeyCredential))) {
    return false;
  }
  const live = () => current() && !controller.signal.aborted;
  if (!live()) return false;
  try {
    const options = await postJSON<PublicKeyOptions>("/api/passkeys/sign-in/options", {});
    if (!live()) return false;
    const credential = await navigator.credentials.get({
      mediation: "conditional",
      publicKey: requestOptions(options),
      signal: controller.signal,
    });
    if (!live() || !credential) return false;
    await postRaw("/api/passkeys/sign-in/finish", credentialJSON(credential));
    return current();
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") {
      return false;
    }
    throw err;
  }
}

export async function addPasskey(): Promise<void> {
  await fresh(async () => {
    const options = await postJSON<PublicKeyOptions>("/api/passkeys/options", {});
    const credential = await navigator.credentials.create({ publicKey: creationOptions(options) });
    if (!credential) {
      throw new Error("unsupported");
    }
    await postRaw("/api/passkeys/finish", credentialJSON(credential));
    await signalAdded(creationUserId(options));
  });
}

export async function renamePasskey(id: string, name: string): Promise<void> {
  const response = await fetch(`/api/passkeys/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!response.ok) {
    throw await parseError(response);
  }
}

export async function removePasskey(id: string): Promise<void> {
  await fresh(async () => {
    const response = await fetch(`/api/passkeys/${id}`, { method: "DELETE", credentials: "include" });
    if (!response.ok) {
      throw await parseError(response);
    }
    const body = (await response.json()) as { credential_id?: string };
    if (body.credential_id) {
      await signalRemoved(body.credential_id);
    }
  });
}

export async function endThisSession(): Promise<void> {
  try {
    await fetch("/api/session", { method: "DELETE", credentials: "include" });
  } catch {
    // The prototype has no session service. Sign-out still leaves the app.
  }
}

export async function confirmWithPasskey(): Promise<void> {
  await stepUp();
}

async function stepUp(): Promise<void> {
  const options = await postJSON<PublicKeyOptions>("/api/passkeys/step-up/options", {});
  let credential: Credential | null = null;
  try {
    credential = await navigator.credentials.get({ publicKey: requestOptions(options) });
  } catch (err) {
    if (err instanceof DOMException && (err.name === "NotAllowedError" || err.name === "AbortError")) {
      throw new Error("cancelled", { cause: err });
    }
    throw err;
  }
  if (!credential) {
    throw new Error("cancelled");
  }
  await postRaw("/api/passkeys/step-up/finish", credentialJSON(credential));
}

type CredentialSignal = {
  signalUnknownCredential?: (options: { rpId: string; credentialId: string }) => Promise<void>;
  signalAllAcceptedCredentials?: (options: {
    rpId: string;
    userId: string;
    allAcceptedCredentialIds: string[];
  }) => Promise<void>;
};

async function signalAdded(userId: string | undefined): Promise<void> {
  const signal = (window.PublicKeyCredential as CredentialSignal | undefined)?.signalAllAcceptedCredentials;
  if (!signal || !userId) return;
  let ids: string[] = [];
  try {
    const response = await fetch("/api/passkeys", { credentials: "include" });
    if (!response.ok) return;
    const rows: unknown = await response.json();
    if (!Array.isArray(rows)) return;
    ids = rows.flatMap((row) => {
      if (!row || typeof row !== "object" || !("credential_id" in row)) return [];
      const id = (row as { credential_id?: unknown }).credential_id;
      return typeof id === "string" && id !== "" ? [id] : [];
    });
  } catch {
    return;
  }
  if (ids.length === 0) return;
  try {
    await signal.call(window.PublicKeyCredential, {
      rpId: window.location.hostname,
      userId,
      allAcceptedCredentialIds: ids,
    });
  } catch {
    // The password manager may not support the signal.
  }
}

async function signalRemoved(credentialId: string): Promise<void> {
  const credential = window.PublicKeyCredential as CredentialSignal | undefined;
  if (!credential?.signalUnknownCredential) {
    return;
  }
  try {
    await credential.signalUnknownCredential({ rpId: window.location.hostname, credentialId });
  } catch {
    // The password manager may not support the signal. The passkey is already gone on the server.
  }
}

export class PasskeyRejected extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(status: number, code?: string) {
    super(code ? `passkey ${status}: ${code}` : `passkey ${status}`);
    this.name = "PasskeyRejected";
    this.status = status;
    this.code = code;
  }
}

function isStale(err: unknown): boolean {
  if (err instanceof PasskeyRejected) {
    return err.status === 403 && err.code === "sign in again";
  }
  if (err && typeof err === "object" && "status" in err) {
    const anyErr = err as { status?: unknown; code?: unknown; error?: unknown };
    return anyErr.status === 403 && (anyErr.code === "sign in again" || anyErr.error === "sign in again");
  }
  return false;
}

function isCancelled(err: unknown): boolean {
  if (err instanceof Error) {
    return err.message === "cancelled" || err.name === "NotAllowedError" || err.name === "AbortError";
  }
  return false;
}

export async function fresh<T>(action: () => Promise<T>): Promise<T | undefined> {
  try {
    return await action();
  } catch (err) {
    if (!isStale(err)) {
      throw err;
    }
    try {
      await stepUp();
    } catch (stepUpErr) {
      if (isCancelled(stepUpErr)) {
        return undefined;
      }
      throw stepUpErr;
    }
    return await action();
  }
}

async function parseError(response: Response): Promise<PasskeyRejected> {
  let code: string | undefined;
  try {
    const data = await response.json();
    if (data && typeof data === "object" && "error" in data) {
      code = String(data.error);
    }
  } catch {
    // Non-JSON response
  }
  return new PasskeyRejected(response.status, code);
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await parseError(response);
  }
  return (await response.json()) as T;
}

async function postRaw(path: string, body: unknown): Promise<void> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await parseError(response);
  }
}

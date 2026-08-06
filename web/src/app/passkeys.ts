// Browser half of the passkey ceremonies. The server keeps the challenge.

interface JSONCredential extends Credential {
  toJSON(): unknown;
}

function credentialJSON(credential: Credential): unknown {
  if (!(credential instanceof PublicKeyCredential) || typeof (credential as JSONCredential).toJSON !== "function") {
    throw new Error("cancelled");
  }
  return (credential as JSONCredential).toJSON();
}

let conditionalController: AbortController | null = null;

export function abortConditional(): void {
  if (conditionalController) {
    conditionalController.abort();
    conditionalController = null;
  }
}

type PublicKeyOptions = { publicKey: PublicKeyCredentialCreationOptionsJSON | PublicKeyCredentialRequestOptionsJSON };

function creationOptions(options: PublicKeyOptions): PublicKeyCredentialCreationOptions {
  return PublicKeyCredential.parseCreationOptionsFromJSON(options.publicKey as PublicKeyCredentialCreationOptionsJSON);
}

function requestOptions(options: PublicKeyOptions): PublicKeyCredentialRequestOptions {
  return PublicKeyCredential.parseRequestOptionsFromJSON(options.publicKey as PublicKeyCredentialRequestOptionsJSON);
}

export async function signInWithPasskey(): Promise<void> {
  abortConditional();
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
    throw new Error("cancelled");
  }
  await postRaw("/api/passkeys/link/finish", { token, response: credentialJSON(credential) });
}

export async function conditionalPasskey(current: () => boolean = () => true): Promise<boolean> {
  abortConditional();
  const controller = new AbortController();
  conditionalController = controller;
  const available = window.PublicKeyCredential?.isConditionalMediationAvailable;
  if (!available || !(await available.call(window.PublicKeyCredential))) {
    return false;
  }
  if (!current()) return false;
  try {
    const options = await postJSON<PublicKeyOptions>("/api/passkeys/sign-in/options", {});
    if (!current()) return false;
    const credential = await navigator.credentials.get({
      mediation: "conditional",
      publicKey: requestOptions(options),
      signal: controller.signal,
    });
    if (!current() || !credential) return false;
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
  const once = async () => {
    const options = await postJSON<PublicKeyOptions>("/api/passkeys/options", {});
    const credential = await navigator.credentials.create({ publicKey: creationOptions(options) });
    if (!credential) {
      throw new Error("cancelled");
    }
    await postRaw("/api/passkeys/finish", credentialJSON(credential));
  };
  try {
    await once();
  } catch (err) {
    if (!stale(err)) {
      throw err;
    }
    await stepUp();
    await once();
  }
}

export async function renamePasskey(id: string, name: string): Promise<void> {
  const response = await fetch(`/api/passkeys/${id}`, {
    method: "PATCH",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!response.ok) {
    throw new PasskeyRejected(response.status);
  }
}

export async function removePasskey(id: string): Promise<void> {
  const once = () => fetch(`/api/passkeys/${id}`, { method: "DELETE", credentials: "include" });
  let response = await once();
  if (response.status === 403) {
    await stepUp();
    response = await once();
  }
  if (!response.ok) {
    throw new PasskeyRejected(response.status);
  }
  const body = (await response.json()) as { credential_id?: string };
  if (body.credential_id) {
    await signalRemoved(body.credential_id);
  }
}

export async function endThisSession(): Promise<void> {
  try {
    const response = await fetch("/api/sessions", { credentials: "include" });
    if (!response.ok) {
      return;
    }
    const body: unknown = await response.json();
    if (!Array.isArray(body)) {
      return;
    }
    const current = body.find(isCurrent);
    if (!current) {
      return;
    }
    await fetch(`/api/sessions/${current.id}`, { method: "DELETE", credentials: "include" });
  } catch {
    // The prototype has no session service. Sign-out still leaves the app.
  }
}

async function stepUp(): Promise<void> {
  const options = await postJSON<PublicKeyOptions>("/api/passkeys/step-up/options", {});
  const credential = await navigator.credentials.get({ publicKey: requestOptions(options) });
  if (!credential) {
    throw new Error("cancelled");
  }
  await postRaw("/api/passkeys/step-up/finish", credentialJSON(credential));
}

type CredentialSignal = {
  signalUnknownCredential?: (options: { rpId: string; credentialId: string }) => Promise<void>;
};

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

class PasskeyRejected extends Error {
  readonly status: number;

  constructor(status: number) {
    super(`passkey ${status}`);
    this.status = status;
  }
}

function stale(err: unknown): boolean {
  return err instanceof PasskeyRejected && err.status === 403;
}

function isCurrent(row: unknown): row is { id: string } {
  if (!row || typeof row !== "object") return false;
  return "current" in row && row.current === true && "id" in row && typeof row.id === "string";
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new PasskeyRejected(response.status);
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
    throw new PasskeyRejected(response.status);
  }
}

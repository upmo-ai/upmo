export const goalMax = 200;
export const messageMax = 4000;

const repoSlug = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

/** Why a goal cannot be sent, or null when it can. Length counts what was typed. */
export function goalProblem(goal: string): string | null {
  if (goal.trim().length === 0) return `Describe the goal in 1 to ${goalMax} characters.`;
  if (goal.length > goalMax) return `Keep the goal to ${goalMax} characters or fewer.`;
  return null;
}

/** An optional repository: owner/name or an https URL. Empty is fine. */
export function repoProblem(repo: string): string | null {
  const value = repo.trim();
  if (value.length === 0) return null;
  if (repoSlug.test(value)) return null;
  try {
    const url = new URL(value);
    if (url.protocol === "https:" && url.hostname !== "" && url.pathname.length > 1) return null;
  } catch {
    // Not a URL: falls through to the message below.
  }
  return "Use owner/name or an https URL.";
}

/** Why a message to a task cannot be sent, or null when it can. */
export function messageProblem(body: string): string | null {
  if (body.trim().length === 0) return "Write a message first.";
  if (body.length > messageMax) {
    return `Keep the message to ${messageMax} characters or fewer. It is ${body.length - messageMax} over.`;
  }
  return null;
}

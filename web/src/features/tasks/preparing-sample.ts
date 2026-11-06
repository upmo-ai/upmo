import type { Preparing } from "./types";

// Stub payloads for the preparing block, one per state a task can be in.

/** Clone is done, toolchain is running and probe is waiting. */
export const preparingRunning: Preparing = {
  steps: [
    {
      name: "clone",
      state: "done",
      message: "Cloned upmo-ai/mail-rules",
      started_at: "2026-11-05T08:15:00.000Z",
      finished_at: "2026-11-05T08:15:03.400Z",
    },
    { name: "toolchain", state: "running", message: null, started_at: "2026-11-05T08:15:03.400Z", finished_at: null },
    { name: "probe", state: "pending", message: null, started_at: null, finished_at: null },
  ],
  runtime: {
    tier: "container",
    repo: "upmo-ai/mail-rules",
    toolchain: "python 3.13",
    limits: { cpu: 1, memory_mb: 512, timeout_s: 600 },
  },
};

/** Clone and toolchain are done and the probe failed. */
export const preparingProbeFailed: Preparing = {
  steps: [
    {
      name: "clone",
      state: "done",
      message: "Cloned upmo-ai/upmo",
      started_at: "2026-11-05T09:10:00.000Z",
      finished_at: "2026-11-05T09:10:04.200Z",
    },
    {
      name: "toolchain",
      state: "done",
      message: "node 24 is on the path",
      started_at: "2026-11-05T09:10:04.200Z",
      finished_at: "2026-11-05T09:10:05.100Z",
    },
    {
      name: "probe",
      state: "failed",
      message: "The work directory is not writable.",
      started_at: "2026-11-05T09:10:05.100Z",
      finished_at: "2026-11-05T09:10:05.700Z",
    },
  ],
  runtime: {
    tier: "kata",
    repo: "upmo-ai/upmo",
    toolchain: "node 24",
    limits: { cpu: 2, memory_mb: 4096, timeout_s: 1800 },
  },
};

/** Every step is done, with a repository and limits. */
export const preparingReady: Preparing = {
  steps: [
    {
      name: "clone",
      state: "done",
      message: "Cloned upmo-ai/finance-digest",
      started_at: "2026-11-05T08:00:00.000Z",
      finished_at: "2026-11-05T08:00:02.000Z",
    },
    {
      name: "toolchain",
      state: "done",
      message: null,
      started_at: "2026-11-05T08:00:02.000Z",
      finished_at: "2026-11-05T08:00:03.000Z",
    },
    {
      name: "probe",
      state: "done",
      message: null,
      started_at: "2026-11-05T08:00:03.000Z",
      finished_at: "2026-11-05T08:00:04.000Z",
    },
  ],
  runtime: {
    tier: "gvisor",
    repo: "upmo-ai/finance-digest",
    toolchain: "python 3.13",
    limits: { cpu: 1, memory_mb: 1024, timeout_s: 900 },
  },
};

/** Every step is done, with no repository, no toolchain and no limits reported. */
export const preparingReadyBare: Preparing = {
  steps: [
    {
      name: "clone",
      state: "done",
      message: "No repository to clone",
      started_at: "2026-11-05T07:30:00.000Z",
      finished_at: "2026-11-05T07:30:00.300Z",
    },
    {
      name: "toolchain",
      state: "done",
      message: null,
      started_at: "2026-11-05T07:30:00.300Z",
      finished_at: "2026-11-05T07:30:01.100Z",
    },
    {
      name: "probe",
      state: "done",
      message: null,
      started_at: "2026-11-05T07:30:01.100Z",
      finished_at: "2026-11-05T07:30:02.000Z",
    },
  ],
  runtime: { tier: "container", repo: null, toolchain: null, limits: null },
};

/**
 * What the server reports once preparation is over: every step done, with the
 * ones that had not run yet starting where the last known one ended and taking
 * a second each. Steps that already finished are left as they were.
 */
export function finishPreparing(preparing: Preparing) {
  const known = preparing.steps
    .flatMap((step) => [step.started_at, step.finished_at])
    .map((stamp) => (stamp === null ? Number.NaN : Date.parse(stamp)))
    .filter((value) => !Number.isNaN(value));
  let cursor = known.length > 0 ? Math.max(...known) : Date.now();
  for (const step of preparing.steps) {
    if (step.finished_at === null) {
      step.started_at ??= new Date(cursor).toISOString();
      cursor = Date.parse(step.started_at) + 1000;
      step.finished_at = new Date(cursor).toISOString();
    }
    step.state = "done";
  }
}

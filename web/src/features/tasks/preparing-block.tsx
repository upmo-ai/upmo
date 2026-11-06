import { Text, Title } from "@mantine/core";
import { Panel, StatusAlert, StatusDot, type Status } from "@upmo-ai/components";

import {
  formatCpu,
  formatDuration,
  formatMemory,
  formatTimeout,
  isPreparingOpen,
  stateLabel,
  stepHint,
  stepLabel,
  stepMillis,
  tierLabel,
  totalMillis,
} from "./preparing";
import type { PrepareStep, PrepareStepState, Preparing, Runtime, Task } from "./types";

import "./preparing-block.css";

const dotStatus: Record<PrepareStepState, Status> = {
  pending: "Queued",
  running: "Working",
  done: "Done",
  failed: "Failed",
};

/**
 * Where the task's sandbox is in getting ready, and what it runs with. It shows
 * what the payload says and keeps no state of its own. While the task is
 * Preparing it lists the steps, afterwards it is a one line summary.
 */
export function PreparingBlock({ task }: { task: Pick<Task, "phase" | "preparing"> }) {
  const { preparing } = task;
  if (!preparing) return null;
  return isPreparingOpen(task, preparing) ? (
    <PreparingSteps preparing={preparing} />
  ) : (
    <PreparingSummary preparing={preparing} />
  );
}

function PreparingSteps({ preparing }: { preparing: Preparing }) {
  return (
    <Panel p="md" component="section" aria-labelledby="task-preparing-title" className="task-preparing">
      <Title order={2} id="task-preparing-title" className="task-section-heading">
        Preparing
      </Title>
      <ol className="task-prepare-steps" aria-label="Preparation steps" aria-live="polite">
        {preparing.steps.map((step) => (
          <PrepareStepRow key={step.name} step={step} />
        ))}
      </ol>
      <RuntimeSettings runtime={preparing.runtime} />
    </Panel>
  );
}

function PrepareStepRow({ step }: { step: PrepareStep }) {
  const took = stepMillis(step);
  return (
    <li className="task-prepare-step" data-state={step.state}>
      {/* The state is written out next to the name, so the dot is only a visual mark. */}
      <span className="task-prepare-step-dot" aria-hidden="true">
        <StatusDot status={dotStatus[step.state]} label={stateLabel[step.state]} />
      </span>
      <div className="task-prepare-step-body">
        <div className="task-prepare-step-head">
          <span className="task-prepare-step-name">{stepLabel[step.name]}</span>
          <span className="task-prepare-step-state">{stateLabel[step.state]}</span>
          {took !== null ? <span className="task-prepare-step-time">{formatDuration(took)}</span> : null}
        </div>
        <Text size="sm" c="dimmed">
          {stepHint[step.name]}
        </Text>
        {step.state === "failed" ? (
          <StatusAlert tone="critical" className="task-prepare-step-note">
            {step.message ?? `${stepLabel[step.name]} failed.`}
          </StatusAlert>
        ) : step.message ? (
          <Text size="sm" className="task-prepare-step-note">
            {step.message}
          </Text>
        ) : null}
      </div>
    </li>
  );
}

function PreparingSummary({ preparing }: { preparing: Preparing }) {
  const total = totalMillis(preparing);
  return (
    <Panel p="md" component="section" aria-labelledby="task-preparing-title" className="task-preparing">
      <Title order={2} id="task-preparing-title" className="task-section-heading">
        Prepared
      </Title>
      <Text size="sm" data-testid="preparing-summary">
        {total === null ? "The workspace was ready." : `The workspace was ready in ${formatDuration(total)}.`}
      </Text>
      <RuntimeSettings runtime={preparing.runtime} />
    </Panel>
  );
}

function RuntimeSettings({ runtime }: { runtime: Runtime }) {
  const rows: [string, string][] = [
    ["Tier", tierLabel[runtime.tier]],
    ["Repository", runtime.repo ?? "None"],
    ["Toolchain", runtime.toolchain ?? "None"],
  ];
  if (runtime.limits) {
    rows.push(
      ["CPU", formatCpu(runtime.limits.cpu)],
      ["Memory", formatMemory(runtime.limits.memory_mb)],
      ["Timeout", formatTimeout(runtime.limits.timeout_s)],
    );
  } else {
    rows.push(["Limits", "Not set"]);
  }
  return (
    <section aria-labelledby="task-runtime-title" className="task-runtime">
      <Title order={3} id="task-runtime-title" className="task-runtime-title">
        Runtime settings
      </Title>
      <dl className="task-runtime-list">
        {rows.map(([name, value]) => (
          <div key={name} className="task-runtime-row">
            <dt>{name}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

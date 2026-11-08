import { Group, Stack, Text } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Button, Dialog, Input, Textarea } from "@upmo-ai/components";

import { BrainError, TASKS_KEY, useApi, useWrite } from "../../app/api";
import { goalMax, goalProblem, repoProblem } from "./rules";

const sandboxMissing =
  "This task needs a sandbox and none is set up yet. Ask an admin to configure one, then try again.";

function createProblem(error: Error) {
  if (error instanceof BrainError && error.status === 503) return sandboxMissing;
  return error.message;
}

export function NewTaskDialog({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  if (!opened) return null;
  return (
    <Dialog opened onClose={onClose} title="New task" withCloseButton={false}>
      <NewTaskForm onClose={onClose} />
    </Dialog>
  );
}

function NewTaskForm({ onClose }: { onClose: () => void }) {
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [goal, setGoal] = useState("");
  const [repo, setRepo] = useState("");
  const [touched, setTouched] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [failure, setFailure] = useState("");
  const [repoFocus, setRepoFocus] = useState(0);
  const advancedRef = useRef<HTMLDivElement>(null);

  // An invalid repository opens Advanced, so move focus to the field that needs fixing.
  useEffect(() => {
    if (repoFocus > 0) advancedRef.current?.querySelector("input")?.focus();
  }, [repoFocus]);

  const create = useWrite(
    () => api.createTask(goal.trim(), repo.trim() === "" ? undefined : repo.trim()),
    [TASKS_KEY],
    {
      shown: true,
      onSuccess: async (created) => {
        onClose();
        await navigate({ to: "/tasks/$taskId", params: { taskId: created.id } });
      },
      onError: async (cause: Error) => {
        setFailure(createProblem(cause));
        // The brain may have made the task before it refused (a 503 leaves it Failed), so the list is stale.
        await queryClient.invalidateQueries({ queryKey: TASKS_KEY });
      },
    },
  );

  const goalError = goalProblem(goal);
  const repoError = repoProblem(repo);
  const showGoalError = goalError !== null && (touched || goal.length > 0);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (goalError || create.isPending) return;
    if (repoError) {
      setAdvanced(true);
      setRepoFocus((count) => count + 1);
      return;
    }
    setFailure("");
    create.mutate();
  };

  return (
    <form onSubmit={submit} noValidate>
      <Stack gap="md">
        <Textarea
          label="Goal"
          placeholder="What should Upmo get done?"
          description={`${goal.length} of ${goalMax} characters`}
          required
          data-autofocus
          minRows={3}
          value={goal}
          error={showGoalError ? goalError : undefined}
          onChange={(event) => {
            setGoal(event.currentTarget.value);
            setFailure("");
          }}
          onBlur={() => setTouched(true)}
        />
        <div>
          <Button
            type="button"
            variant="subtle"
            aria-expanded={advanced}
            aria-controls="new-task-advanced"
            onClick={() => setAdvanced(!advanced)}
          >
            Advanced
          </Button>
        </div>
        {advanced ? (
          <div id="new-task-advanced" ref={advancedRef}>
            <Input
              label="Repository"
              description="Optional. Use owner/name or an https URL."
              placeholder="upmo-ai/upmo"
              value={repo}
              error={repo.trim() !== "" ? repoError : undefined}
              onChange={(event) => {
                setRepo(event.currentTarget.value);
                setFailure("");
              }}
            />
          </div>
        ) : null}
        {failure ? (
          <Text role="alert" c="red" size="sm">
            {failure}
          </Text>
        ) : null}
        <Group gap="xs">
          <Button type="submit" disabled={create.isPending || goalError !== null}>
            Create task
          </Button>
          <Button type="button" variant="default" onClick={onClose}>
            Cancel
          </Button>
        </Group>
      </Stack>
    </form>
  );
}

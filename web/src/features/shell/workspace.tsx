import { Text } from "@mantine/core";

/** The word people read for this install. One install, so the name is a label. */
export const workspaceName = "Aster";

export function WorkspaceName() {
  return (
    <Text className="shell-workspace" fw={600}>
      {workspaceName}
    </Text>
  );
}

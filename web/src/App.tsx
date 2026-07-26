import { Container, Stack, Text, Title } from "@mantine/core";

import { ThemeSwitch } from "./theme";

export function App() {
  return (
    <Container size="sm" py="xl">
      <Stack gap="xs">
        <Title order={1}>Upmo</Title>
        <Text size="lg">Always up.</Text>
        <Text c="dimmed">Web app placeholder</Text>
        <ThemeSwitch />
      </Stack>
    </Container>
  );
}

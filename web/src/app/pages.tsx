import { Section } from "../ui/section";

export function HomePage() {
  return (
    <Section
      title="Home"
      emptyTitle="Your workspace"
      emptyBody="Open a section from the sidebar to see what is happening here."
    />
  );
}

export function ChatPage() {
  return (
    <Section
      title="Chat"
      emptyTitle="Start a chat"
      emptyBody="Ask a question, paste a file, or describe what you want done."
    />
  );
}

export function AutomationsPage() {
  return (
    <Section
      title="Automations"
      emptyTitle="Run tasks on a schedule"
      emptyBody="A daily digest, a weekly review, a check every hour."
    />
  );
}

export function AgentsPage() {
  return (
    <Section
      title="Agents"
      emptyTitle="Add an agent"
      emptyBody="Agents run tasks with the tools you give them."
    />
  );
}

export function KnowledgePage() {
  return (
    <Section
      title="Knowledge"
      emptyTitle="Give Upmo something to read"
      emptyBody="Upload files or point to a folder. Agents search it when it helps."
    />
  );
}

export function UsagePage() {
  return (
    <Section
      title="Usage"
      emptyTitle="Usage shows up here"
      emptyBody="Token counts and cost appear after a task runs."
    />
  );
}

import { Section } from "../ui/section";

export function HomePage() {
  return (
    <Section
      title="Home"
      emptyTitle="Your first task"
      emptyBody="Give Upmo a goal. It plans the work, does it and checks the result while you do something else."
      emptyAction={{ label: "Create task", to: "/tasks" }}
    />
  );
}

export function ChatPage() {
  return (
    <Section
      title="Chat"
      emptyTitle="Start a chat"
      emptyBody="Ask a question, paste a file, or describe what you want done."
      emptyAction={{ label: "New chat", to: "/chat" }}
    />
  );
}

export function AutomationsPage() {
  return (
    <Section
      title="Automations"
      emptyTitle="Run tasks on a schedule"
      emptyBody="A daily digest, a weekly review, a check every hour."
      emptyAction={{ label: "Add automation", to: "/automations" }}
    />
  );
}

export function AgentsPage() {
  return (
    <Section
      title="Agents"
      emptyTitle="Add an agent"
      emptyBody="Agents run tasks with the tools you give them."
      emptyAction={{ label: "Add agent", to: "/agents" }}
    />
  );
}

export function KnowledgePage() {
  return (
    <Section
      title="Knowledge"
      emptyTitle="Give Upmo something to read"
      emptyBody="Upload files or point to a folder. Agents search it when it helps."
      emptyAction={{ label: "Add source", to: "/knowledge" }}
    />
  );
}

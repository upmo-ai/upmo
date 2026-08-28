export type Source = {
  id: string;
  name: string;
};

export type Folder = {
  id: string;
  name: string;
  summary: string;
  updated: string;
  sources: Source[];
};

export const sampleFolders: Folder[] = [
  {
    id: "folder-product",
    name: "Product",
    summary: "Handbook, pricing and the glossary for written work.",
    updated: "2026-08-30",
    sources: [
      { id: "src-handbook", name: "Handbook.pdf" },
      { id: "src-pricing", name: "Pricing.md" },
    ],
  },
  {
    id: "folder-policies",
    name: "Policies",
    summary: "Access rules and what a person may do in this workspace.",
    updated: "2026-08-30",
    sources: [{ id: "src-access", name: "Access.md" }],
  },
  {
    id: "folder-finance",
    name: "Finance",
    summary: "Close calendar, ledger notes and the monthly digest sources.",
    updated: "2026-09-28",
    sources: [
      { id: "src-close", name: "Close.md" },
      { id: "src-ledger", name: "Ledger.md" },
    ],
  },
  {
    id: "folder-research",
    name: "Research",
    summary: "Notes and papers an agent may retrieve during a review.",
    updated: "2026-10-13",
    sources: [{ id: "src-notes", name: "Notes.md" }],
  },
  {
    id: "folder-legal",
    name: "Legal",
    summary: "Terms, privacy and the words we use in the product.",
    updated: "2026-10-11",
    sources: [
      { id: "src-terms", name: "Terms.md" },
      { id: "src-privacy", name: "Privacy.md" },
    ],
  },
  {
    id: "folder-engineering",
    name: "Engineering",
    summary: "How we ship: pipelines, databases and the runtime.",
    updated: "2026-08-30",
    sources: [
      { id: "src-pipelines", name: "Pipelines.md" },
      { id: "src-runtime", name: "Runtime.md" },
    ],
  },
];

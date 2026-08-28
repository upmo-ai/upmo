export type Fact = {
  id: string;
  text: string;
  source: string;
  forgotten: boolean;
  replaced: boolean;
};

export const sampleProfile =
  "Prefers short written answers in British English. Works across finance close and product reviews. Asks before a change leaves the workspace.";

export const sampleFacts: Fact[] = [
  {
    id: "fact-spelling",
    text: "Prefers British spelling in written work.",
    source: "Learned in chat · today",
    forgotten: false,
    replaced: false,
  },
  {
    id: "fact-close",
    text: "Finance close is the last Friday of the month.",
    source: "Learned in Daily finance digest · run 9",
    forgotten: false,
    replaced: false,
  },
  {
    id: "fact-glossary",
    text: "Use the product glossary in every reply.",
    source: "Learned in Research · run 4",
    forgotten: false,
    replaced: false,
  },
];

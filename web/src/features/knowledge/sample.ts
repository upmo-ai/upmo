export type Source = {
  id: string;
  name: string;
};

export type Folder = {
  id: string;
  name: string;
  sources: Source[];
};

export const sampleFolders: Folder[] = [
  {
    id: "folder-product",
    name: "Product",
    sources: [
      { id: "src-handbook", name: "Handbook.pdf" },
      { id: "src-pricing", name: "Pricing.md" },
    ],
  },
  {
    id: "folder-policies",
    name: "Policies",
    sources: [{ id: "src-access", name: "Access.md" }],
  },
];

import { useMemo, type ReactNode } from "react";

import { parseMarkdown, type Block, type Inline } from "./markdown";

// The page already has an h1 and the viewer an h3, so a file's headings start at h4.
const headingTags = ["h4", "h5", "h6", "h6", "h6", "h6"] as const;

function inline(nodes: Inline[]): ReactNode[] {
  return nodes.map((node, index) => {
    switch (node.type) {
      case "text":
        return node.value;
      case "code":
        return <code key={index}>{node.value}</code>;
      case "strong":
        return <strong key={index}>{inline(node.children)}</strong>;
      case "em":
        return <em key={index}>{inline(node.children)}</em>;
      case "link":
        return (
          <a key={index} href={node.href} target="_blank" rel="noopener noreferrer nofollow" referrerPolicy="no-referrer">
            {inline(node.children)}
            <span className="artifact-md-hidden"> (opens in a new tab)</span>
          </a>
        );
    }
  });
}

function blocks(nodes: Block[]): ReactNode[] {
  return nodes.map((node, index) => {
    switch (node.type) {
      case "heading": {
        const Tag = headingTags[node.level - 1] ?? "h6";
        return <Tag key={index}>{inline(node.children)}</Tag>;
      }
      case "paragraph":
        return <p key={index}>{inline(node.children)}</p>;
      case "code":
        return (
          <pre key={index}>
            <code>{node.value}</code>
          </pre>
        );
      case "quote":
        return <blockquote key={index}>{blocks(node.children)}</blockquote>;
      case "rule":
        return <hr key={index} />;
      case "list": {
        const items = node.items.map((item, at) => (
          <li key={at}>
            {inline(item.children)}
            {blocks(item.nested)}
          </li>
        ));
        return node.ordered ? <ol key={index}>{items}</ol> : <ul key={index}>{items}</ul>;
      }
      case "table":
        return (
          <div key={index} className="artifact-md-scroll">
            <table>
              <thead>
                <tr>
                  {node.header.map((cell, at) => (
                    <th key={at} scope="col">
                      {inline(cell)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {node.rows.map((row, at) => (
                  <tr key={at}>
                    {row.map((cell, column) => (
                      <td key={column}>{inline(cell)}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
    }
  });
}

/** Markdown as elements. Nothing here sets innerHTML, so file text cannot become markup. */
export function MarkdownView({ source }: { source: string }) {
  const tree = useMemo(() => parseMarkdown(source), [source]);
  return <div className="artifact-markdown">{blocks(tree)}</div>;
}

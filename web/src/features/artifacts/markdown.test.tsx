import { cleanup, render } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";

import { parseMarkdown, safeHref } from "./markdown";
import { MarkdownView } from "./markdown-view";

const payload = [
  "# Report",
  "",
  "<script>window.pwned = 1</script>",
  "",
  '<img src=x onerror="window.pwned = 1">',
  "",
  "[click](javascript:window.pwned=1) and [again](JaVa\tScRiPt:window.pwned=2) and [data](data:text/html;base64,PHNjcmlwdD4=)",
  "",
  "![tracker](https://evil.example/pixel.png)",
  "",
  "<iframe src=\"https://evil.example\"></iframe> <a href=\"javascript:window.pwned=3\">raw</a>",
].join("\n");

function html(source: string) {
  return renderToStaticMarkup(<MarkdownView source={source} />);
}

afterEach(() => {
  cleanup();
});

describe("markdown sanitiser", () => {
  it("UPM-74 shows a script payload as text and emits no markup from it", () => {
    const out = html(payload);
    expect(out).not.toMatch(/<script/i);
    expect(out).not.toMatch(/<img/i);
    expect(out).not.toMatch(/<iframe/i);
    expect(out).not.toMatch(/<[^>]*\sonerror/i);
    expect(out).not.toMatch(/href="javascript/i);
    expect(out).not.toMatch(/href="data:/i);
    expect(out).not.toContain("<a ");
    // The payload is still readable, as characters.
    expect(out).toContain("&lt;script&gt;window.pwned = 1&lt;/script&gt;");
    expect(out).toContain("click");
  });

  it("UPM-74 puts no script, image, frame or handler in the live document", () => {
    const { container } = render(<MarkdownView source={payload} />);
    expect(container.querySelector("script, img, iframe, object, embed, a")).toBeNull();
    expect(container.querySelector("[onerror], [onclick], [onload]")).toBeNull();
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined();
    expect(container.textContent).toContain("<script>window.pwned = 1</script>");
  });

  it("UPM-74 keeps markup inside code as text", () => {
    const out = html("```html\n<script>alert(1)</script>\n```\n\nand `<b onclick=x>` inline");
    expect(out).not.toMatch(/<script/i);
    expect(out).not.toMatch(/<b /i);
    expect(out).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
  });

  it("UPM-74 keeps a link only when it is http, https or mailto", () => {
    expect(safeHref("https://example.com/a?b=1")).toBe("https://example.com/a?b=1");
    expect(safeHref("HTTP://example.com")).toBe("HTTP://example.com");
    expect(safeHref("mailto:ada@example.com")).toBe("mailto:ada@example.com");
    for (const bad of [
      "javascript:alert(1)",
      "JaVaScRiPt:alert(1)",
      " \tjava\nscript:alert(1)",
      "&#x6A;avascript:alert(1)",
      "data:text/html,<script>1</script>",
      "vbscript:msgbox",
      "file:///etc/passwd",
      "//evil.example",
      "/relative",
      "#anchor",
      "",
    ]) {
      expect([bad, safeHref(bad)]).toEqual([bad, null]);
    }
  });

  it("UPM-74 opens a safe link in a new tab without an opener or referrer", () => {
    const out = html("[docs](https://example.com/docs)");
    expect(out).toContain('href="https://example.com/docs"');
    expect(out).toContain('target="_blank"');
    expect(out).toContain('rel="noopener noreferrer nofollow"');
    expect(out).toContain('referrerPolicy="no-referrer"');
  });

  it("UPM-74 tells a screen reader that a link opens in a new tab", () => {
    expect(html("[docs](https://example.com/docs)")).toContain("docs<span");
    expect(html("[docs](https://example.com/docs)")).toContain("(opens in a new tab)</span></a>");
  });

  it("UPM-74 drops the address of an unsafe link and keeps its label", () => {
    const tree = parseMarkdown("[open](javascript:alert(1))");
    expect(tree).toEqual([{ type: "paragraph", children: [{ type: "text", value: "open" }] }]);
  });

  it("UPM-74 does not load an image from Markdown and shows its alt text", () => {
    const out = html("![a chart](https://evil.example/pixel.png)");
    expect(out).not.toContain("<img");
    expect(out).not.toContain("evil.example");
    expect(out).toContain("a chart");
  });
});

describe("markdown reader", () => {
  it("UPM-74 reads headings, lists, emphasis, tables and quotes", () => {
    const out = html(
      [
        "## Totals",
        "",
        "Spend is **up** and *steady*, see `ledger`.",
        "",
        "- one",
        "  - nested",
        "- two",
        "",
        "1. first",
        "2. second",
        "",
        "| Vendor | Amount |",
        "| --- | --- |",
        "| Hetzner | 2140 |",
        "",
        "> quoted",
      ].join("\n"),
    );
    expect(out).toContain("<h5>Totals</h5>");
    expect(out).toContain("<strong>up</strong>");
    expect(out).toContain("<em>steady</em>");
    expect(out).toContain("<code>ledger</code>");
    expect(out).toContain("<li>one<ul><li>nested</li></ul></li>");
    expect(out).toContain("<ol><li>first</li><li>second</li></ol>");
    expect(out).toContain('<th scope="col">Vendor</th>');
    expect(out).toContain("<td>2140</td>");
    expect(out).toContain("<blockquote><p>quoted</p></blockquote>");
  });

  it("UPM-74 does not turn snake_case or a spaced asterisk into emphasis", () => {
    const out = html("use snake_case_name and 2 * 3 * 4");
    expect(out).not.toContain("<em>");
    expect(out).toContain("snake_case_name");
  });

  it("UPM-74 reads an unclosed fence to the end of the file", () => {
    const tree = parseMarkdown("```py\nprint(1)\nprint(2)");
    expect(tree).toEqual([{ type: "code", language: "py", value: "print(1)\nprint(2)" }]);
  });

  it("UPM-74 finishes on input built to be slow", () => {
    const inputs = [
      "*a ".repeat(60_000),
      "[".repeat(100_000),
      "`".repeat(100_000),
      "> ".repeat(5_000) + "deep",
      "- x\n".repeat(5_000),
      Array.from({ length: 200 }, (_, depth) => `${" ".repeat(depth * 2)}- level ${depth}`).join("\n"),
      "# " + " ".repeat(100_000) + "x",
      "| a |\n".repeat(20_000),
    ];
    for (const input of inputs) {
      expect(html(input).length).toBeGreaterThan(0);
    }
  });
});

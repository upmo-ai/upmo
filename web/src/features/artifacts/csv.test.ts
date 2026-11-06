import { describe, expect, it } from "vitest";

import { parseCsv } from "./csv";

describe("csv", () => {
  it("UPM-74 keeps a quoted comma inside its cell", () => {
    expect(parseCsv('Vendor,Amount\nHetzner,"€2,140.00"')).toEqual([
      ["Vendor", "Amount"],
      ["Hetzner", "€2,140.00"],
    ]);
  });

  it("UPM-74 reads doubled quotes and line breaks inside a quoted cell", () => {
    expect(parseCsv('a,"say ""hi""","one\ntwo"')).toEqual([["a", 'say "hi"', "one\ntwo"]]);
  });

  it("UPM-74 handles CRLF, a trailing line break and blank lines", () => {
    expect(parseCsv("a,b\r\n\r\nc,d\r\n")).toEqual([
      ["a", "b"],
      ["c", "d"],
    ]);
  });

  it("UPM-74 keeps empty cells and reads an empty file as no rows", () => {
    expect(parseCsv("a,,c\n,\n")).toEqual([
      ["a", "", "c"],
      ["", ""],
    ]);
    expect(parseCsv("")).toEqual([]);
    expect(parseCsv("\n\n")).toEqual([]);
  });

  it("UPM-74 keeps a quote in the middle of an unquoted cell", () => {
    expect(parseCsv('5" pipe,ok')).toEqual([['5" pipe', "ok"]]);
  });
});

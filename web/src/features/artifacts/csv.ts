/**
 * Reads comma separated values (RFC 4180): a quoted field may hold commas,
 * line breaks and doubled quotes. A trailing line break adds no empty row.
 */
export function parseCsv(source: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  let started = false;

  const endField = () => {
    row.push(field);
    field = "";
    started = false;
  };
  const endRow = () => {
    // A blank line is not a row.
    if (row.length === 0 && field === "" && !started) return;
    endField();
    rows.push(row);
    row = [];
  };

  for (let i = 0; i < source.length; i += 1) {
    const char = source.charAt(i);
    if (quoted) {
      if (char === '"') {
        if (source.charAt(i + 1) === '"') {
          field += '"';
          i += 1;
        } else {
          quoted = false;
        }
      } else {
        field += char;
      }
      continue;
    }
    if (char === '"' && field === "") {
      quoted = true;
      started = true;
    } else if (char === ",") {
      endField();
      started = true;
    } else if (char === "\n" || char === "\r") {
      if (char === "\r" && source.charAt(i + 1) === "\n") i += 1;
      endRow();
    } else {
      field += char;
      started = true;
    }
  }
  if (started || field !== "" || row.length > 0) endRow();
  return rows;
}

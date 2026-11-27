// Renders what tmux captures of a pane (text with ANSI SGR sequences) as HTML.

const PALETTE = [
  "#2b2f36", "#ff6b6b", "#5fd38d", "#f2c14e", "#6cb6ff", "#c58cff", "#5fd3d3", "#c9ced6",
  "#6b7380", "#ff8787", "#7ee2a8", "#ffd479", "#8cc8ff", "#d8a8ff", "#7fe6e6", "#f2f4f7",
]; // prettier-ignore

/** One of the 256 indexed terminal colours. */
function indexed(n: number): string {
  if (n < 16) return PALETTE[n] ?? "";
  if (n < 232) {
    const level = (x: number) => (x ? x * 40 + 55 : 0);
    const c = n - 16;
    return `rgb(${String(level(Math.floor(c / 36)))},${String(level(Math.floor(c / 6) % 6))},${String(level(c % 6))})`;
  }
  const grey = 8 + (n - 232) * 10;
  return `rgb(${String(grey)},${String(grey)},${String(grey)})`;
}

const ESCAPES: Record<string, string> = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
};

function escapeHtml(text: string): string {
  return text.replace(/[&<>"']/g, (c) => ESCAPES[c] ?? c);
}

interface Style {
  fg?: string | undefined;
  bg?: string | undefined;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  inverse?: boolean;
  strike?: boolean;
}

function css(style: Style): string {
  let { fg, bg } = style;
  if (style.inverse) [fg, bg] = [bg ?? "#090b0d", fg ?? "#d7dae0"];
  const rules: string[] = [];
  if (fg) rules.push(`color:${fg}`);
  if (bg) rules.push(`background:${bg}`);
  if (style.bold) rules.push("font-weight:700");
  if (style.dim) rules.push("opacity:.55");
  if (style.italic) rules.push("font-style:italic");
  if (style.underline || style.strike) {
    const lines = [style.underline ? "underline" : "", style.strike ? "line-through" : ""];
    rules.push(`text-decoration:${lines.join(" ").trim()}`);
  }
  return rules.join(";");
}

/**
 * The screen as HTML: 16, 256 and true colours, bold, dim, italic, underline, inverse and
 * strike-through; other escape sequences are dropped and blank lines at the end trimmed.
 */
export function ansiToHtml(screen: string): string {
  const lines = screen
    // eslint-disable-next-line no-control-regex -- terminal output is made of control characters
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "")
    // eslint-disable-next-line no-control-regex
    .replace(/\x1b[()][A-Za-z0-9]/g, "")
    // eslint-disable-next-line no-control-regex
    .replace(/\x1b[=>]/g, "")
    .split("\n");
  // eslint-disable-next-line no-control-regex
  const blank = (line: string) => !line.replace(/\x1b\[[0-9;:]*m/g, "").trim();
  while (lines.length > 0 && blank(lines[lines.length - 1] ?? "")) lines.pop();
  const text = lines.join("\n");

  let out = "";
  let style: Style = {};
  let open = false;
  let last = 0;
  // eslint-disable-next-line no-control-regex
  const sequence = /\x1b\[([0-9;:]*)([A-Za-z])/g;
  for (let match = sequence.exec(text); match; match = sequence.exec(text)) {
    out += escapeHtml(text.slice(last, match.index));
    last = sequence.lastIndex;
    if (match[2] !== "m") continue;
    const codes = match[1] === "" ? [0] : (match[1] ?? "").split(/[;:]/).map((x) => Number(x) || 0);
    for (let i = 0; i < codes.length; i++) {
      const n = codes[i] ?? 0;
      if (n === 0) style = {};
      else if (n === 1) style.bold = true;
      else if (n === 2) style.dim = true;
      else if (n === 3) style.italic = true;
      else if (n === 4) style.underline = true;
      else if (n === 7) style.inverse = true;
      else if (n === 9) style.strike = true;
      else if (n === 22) style.bold = style.dim = false;
      else if (n === 23) style.italic = false;
      else if (n === 24) style.underline = false;
      else if (n === 27) style.inverse = false;
      else if (n === 29) style.strike = false;
      else if (n >= 30 && n <= 37) style.fg = PALETTE[n - 30];
      else if (n === 39) style.fg = undefined;
      else if (n >= 40 && n <= 47) style.bg = PALETTE[n - 40];
      else if (n === 49) style.bg = undefined;
      else if (n >= 90 && n <= 97) style.fg = PALETTE[n - 82];
      else if (n >= 100 && n <= 107) style.bg = PALETTE[n - 92];
      else if (n === 38 || n === 48) {
        let colour: string | undefined;
        if (codes[i + 1] === 5) {
          colour = indexed(codes[i + 2] ?? 0);
          i += 2;
        } else if (codes[i + 1] === 2) {
          colour = `rgb(${String(codes[i + 2] ?? 0)},${String(codes[i + 3] ?? 0)},${String(codes[i + 4] ?? 0)})`;
          i += 4;
        }
        if (n === 38) style.fg = colour;
        else style.bg = colour;
      }
    }
    if (open) {
      out += "</span>";
      open = false;
    }
    const rules = css(style);
    if (rules) {
      out += `<span style="${rules}">`;
      open = true;
    }
  }
  out += escapeHtml(text.slice(last));
  if (open) out += "</span>";
  return out;
}

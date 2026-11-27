import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { tokensCss } from "@devbox/shared";
import type { Tokens } from "@devbox/shared";

const root = new URL("../../", import.meta.url);
const tokens = JSON.parse(readFileSync(new URL("web/shared/tokens.json", root), "utf8")) as Tokens;

// The table in docs/spec/16-design.md.
const design: Record<string, string> = {
  "--bg": "#0b0d10",
  "--bg2": "#101318",
  "--card": "#14181e",
  "--card2": "#1a1f27",
  "--line": "#242a33",
  "--line2": "#2e3540",
  "--text": "#e7e9ec",
  "--dim": "#9198a3",
  "--faint": "#5d6570",
  "--accent": "#7cc4ff",
  "--accent-ink": "#06111c",
  "--ok": "#5fd38d",
  "--warn": "#f2c14e",
  "--bad": "#ff6b6b",
  "--violet": "#b69cff",
  "--screen": "#090b0d",
  "--agent-claude": "#e8916b",
  "--agent-codex": "#8fd3c7",
  "--agent-opencode": "#f2c14e",
  "--agent-shell": "#9198a3",
  "--progress-from": "#4aa3ff",
  "--progress-to": "#7cc4ff",
  "--radius-card": "16px",
  "--radius-control": "12px",
  "--radius-chip": "99px",
  "--radius-badge": "10px",
  "--radius-sheet": "22px",
  "--font-sans": 'ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", Inter, sans-serif',
  "--font-mono": 'ui-monospace, "SF Mono", Menlo, "JetBrains Mono", monospace',
};

test("the CSS variables are the design tokens", () => {
  const css = tokensCss(tokens);
  expect(css.startsWith(":root{") && css.endsWith("}")).toBe(true);
  const variables = Object.fromEntries(
    css
      .slice(":root{".length, -1)
      .split(";")
      .map((declaration) => {
        const colon = declaration.indexOf(":");
        return [declaration.slice(0, colon), declaration.slice(colon + 1)];
      }),
  );
  expect(variables).toEqual(design);
});

test("the built boot screen carries the tokens and uses no colour outside them", () => {
  const page = readFileSync(new URL("internal/gateway/boot.gen.html", root), "utf8");
  expect(page).toContain(tokensCss(tokens));
  const style = /<style>(.*?)<\/style>/s.exec(page)?.[1] ?? "";
  const rules = style.slice(tokensCss(tokens).length);
  // Tints of token colours are written as 8-digit hex; anything else must be a variable.
  const literals = [...rules.matchAll(/#[0-9a-f]{3,8}\b/gi)].map((m) => m[0].toLowerCase());
  const allowed = new Set([...Object.values(tokens.color), "#fff"]);
  for (const literal of literals) {
    expect(literal.length === 9 || allowed.has(literal), literal).toBe(true);
  }
});

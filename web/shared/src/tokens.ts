/** The design tokens in tokens.json: colours by name, radii and font stacks. */
export interface Tokens {
  color: Record<string, string>;
  radius: Record<string, string>;
  font: Record<string, string>;
}

/**
 * The tokens as CSS custom properties on `:root`: a colour `accent` is `--accent`, a radius
 * `card` is `--radius-card`, a font `mono` is `--font-mono`.
 */
export function tokensCss(tokens: Tokens): string {
  const declarations = [
    ...Object.entries(tokens.color).map(([name, value]) => `--${name}:${value}`),
    ...Object.entries(tokens.radius).map(([name, value]) => `--radius-${name}:${value}`),
    ...Object.entries(tokens.font).map(([name, value]) => `--font-${name}:${value}`),
  ];
  return `:root{${declarations.join(";")}}`;
}

import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { GatewayStatus } from "@devbox/shared/proto/types";
import type { Boot } from "@devbox/gateway-ui/App.tsx";

/** One boot screen state in tests/fixtures/boot: what the gateway injected and what it streams. */
export interface BootFixture {
  boot: Boot;
  status?: GatewayStatus;
  /** The gateway's clock when the screen is shown. */
  now?: number;
}

export function fixture(name: string): BootFixture {
  const path = join(import.meta.dirname, "../../fixtures/boot", `${name}.json`);
  return JSON.parse(readFileSync(path, "utf8")) as BootFixture;
}

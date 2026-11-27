import { readdirSync, readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { expect, test } from "vitest";
import type { GatewayHealth, HubWake, SyncProgress } from "@devbox/shared/proto/types";
import * as versions from "@devbox/shared/proto/versions";

const root = new URL("../../", import.meta.url);
const read = (path: string) => readFileSync(new URL(path, root), "utf8");
const json = (path: string) => JSON.parse(read(path)) as unknown;

interface SchemaFile {
  title: string;
  "x-version": number;
}
const contracts = readdirSync(new URL("contracts/", root))
  .filter((file) => file.endsWith(".schema.json"))
  .map((file) => ({
    name: file.replace(".schema.json", ""),
    ...(json(`contracts/${file}`) as SchemaFile),
  }));
const examples = readdirSync(new URL("contracts/examples/", root));

// The bundle the Go and Rust validators embed: every type is in its $defs. Formats are devbox's
// own and are checked by those validators (tests/fixtures/schema/formats.json).
const bundle = json("internal/proto/schema.gen.json") as { $defs: Record<string, unknown> };
const ajv = new Ajv2020({ strict: false, validateFormats: false, allErrors: true });
ajv.addSchema(bundle, "contracts");

test("there are seventeen contracts and each has an example", () => {
  expect(contracts).toHaveLength(17);
  for (const { name } of contracts) {
    expect(examples, name).toContain(`${name}.json`);
  }
});

test.each(examples)("%s matches its schema and round-trips", (file) => {
  const contract = contracts.find(({ name }) => name === file.split(".")[0]);
  if (!contract) throw new Error(`${file} has no schema`);
  const validate = ajv.getSchema(`contracts#/$defs/${contract.title}`);
  if (!validate) throw new Error(`${contract.title} is not in the bundle`);

  const example = json(`contracts/examples/${file}`);
  expect(validate(example), JSON.stringify(validate.errors)).toBe(true);
  expect(JSON.parse(JSON.stringify(example))).toEqual(example);
});

test("the schemas reject what the generated types reject", () => {
  const validate = ajv.getSchema("contracts#/$defs/GatewayHealth");
  if (!validate) throw new Error("GatewayHealth is not in the bundle");
  const health: GatewayHealth = {
    v: versions.GATEWAY_HEALTH_VERSION,
    status: "ok",
    problems: [],
    vm: "stopped",
    phase: "idle",
    checkedAt: 1,
  };
  expect(validate(health)).toBe(true);
  expect(validate({ ...health, status: "fine" })).toBe(false);
  expect(validate({ ...health, checkedAt: undefined })).toBe(false);
  // Unknown fields are allowed: producers may add optional ones without a version bump.
  expect(validate({ ...health, addedLater: true })).toBe(true);
});

test("the generated types describe the examples", () => {
  // Checked by tsc: these assignments fail to compile when a field is renamed or retyped.
  const wake: HubWake = { v: versions.HUB_WAKE_VERSION, now: 1760000006500, kind: "restore" };
  const line: SyncProgress = { v: versions.SYNC_PROGRESS_VERSION, type: "repo", name: "devbox" };
  expect(json("contracts/examples/hub-wake.json")).toEqual(wake);
  expect(json("contracts/examples/sync-progress.repo.json")).toMatchObject(line);
});

test("the version constants are the schemas' versions", () => {
  const constants = versions as Record<string, number>;
  for (const { title, "x-version": version } of contracts) {
    const constant = `${title.replace(/(?<!^)([A-Z])/g, "_$1").toUpperCase()}_VERSION`;
    expect(constants[constant], constant).toBe(version);
  }
  expect(Object.keys(constants)).toHaveLength(contracts.length);
});

test("the app's visual fixtures are messages the hub and the gateway could send", () => {
  const fixture = json("tests/fixtures/app/app.json") as {
    state: unknown;
    gateway: unknown;
    asleep: unknown;
    frames: Record<string, unknown>;
  };
  const check = (type: string, value: unknown) => {
    const validate = ajv.getSchema(`contracts#/$defs/${type}`);
    if (!validate) throw new Error(`${type} is not in the bundle`);
    expect(validate(value), `${type}: ${JSON.stringify(validate.errors)}`).toBe(true);
  };
  check("HubState", fixture.state);
  check("GatewayStatus", fixture.gateway);
  check("GatewayStatus", fixture.asleep);
  for (const frame of Object.values(fixture.frames)) check("ScreenFrame", frame);
});

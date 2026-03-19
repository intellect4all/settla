/**
 * Export the OpenAPI spec from the gateway to a JSON file.
 * Usage: tsx scripts/export-openapi.ts [output-path]
 */
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

// Set required env vars before importing buildApp
process.env.VITEST = "1";
process.env.SETTLA_JWT_SECRET ??= "export-openapi-dummy-secret";

const { buildApp } = await import("../src/index.js");

const app = await buildApp({
  grpc: {} as any,
  redis: null,
  resolveTenant: async () => null,
  skipGrpcPool: true,
});

await app.ready();

const spec = app.swagger();
const outputPath = resolve(process.argv[2] ?? "openapi.json");

writeFileSync(outputPath, JSON.stringify(spec, null, 2) + "\n");
console.log(`OpenAPI spec written to ${outputPath}`);

await app.close();
process.exit(0);

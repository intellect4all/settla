import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    coverage: {
      provider: "v8",
      thresholds: {
        statements: 50,
        lines: 50,
        functions: 50,
        branches: 40,
      },
    },
  },
});

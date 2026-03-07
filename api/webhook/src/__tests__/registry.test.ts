import { describe, it, expect, beforeEach } from "vitest";
import { WebhookRegistry } from "../registry.js";
import type { WebhookRegistration } from "../types.js";

function makeReg(overrides: Partial<WebhookRegistration> = {}): WebhookRegistration {
  return {
    id: "wh_1",
    tenantId: "tenant_a",
    url: "https://example.com/webhook",
    secret: "secret_1",
    events: [],
    isActive: true,
    ...overrides,
  };
}

describe("WebhookRegistry", () => {
  let registry: WebhookRegistry;

  beforeEach(() => {
    registry = new WebhookRegistry();
  });

  it("registers and retrieves a webhook", () => {
    const reg = makeReg();
    registry.register(reg);
    expect(registry.getById("wh_1")).toEqual(reg);
  });

  it("returns matching registrations for a tenant", () => {
    registry.register(makeReg({ id: "wh_1", tenantId: "t1" }));
    registry.register(makeReg({ id: "wh_2", tenantId: "t2" }));
    const matches = registry.getMatchingRegistrations("t1", "transfer.completed");
    expect(matches).toHaveLength(1);
    expect(matches[0].id).toBe("wh_1");
  });

  it("empty events array matches all event types", () => {
    registry.register(makeReg({ events: [] }));
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.completed")).toHaveLength(1);
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.failed")).toHaveLength(1);
  });

  it("filters by event type when events are specified", () => {
    registry.register(makeReg({ events: ["transfer.completed"] }));
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.completed")).toHaveLength(1);
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.failed")).toHaveLength(0);
  });

  it("excludes inactive registrations", () => {
    registry.register(makeReg({ isActive: false }));
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.completed")).toHaveLength(0);
  });

  it("unregisters a webhook", () => {
    registry.register(makeReg());
    expect(registry.unregister("wh_1")).toBe(true);
    expect(registry.getById("wh_1")).toBeUndefined();
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.completed")).toHaveLength(0);
  });

  it("returns false when unregistering non-existent webhook", () => {
    expect(registry.unregister("nonexistent")).toBe(false);
  });

  it("supports multiple webhooks per tenant", () => {
    registry.register(makeReg({ id: "wh_1" }));
    registry.register(makeReg({ id: "wh_2" }));
    expect(registry.getMatchingRegistrations("tenant_a", "transfer.completed")).toHaveLength(2);
  });

  it("returns empty for unknown tenant", () => {
    expect(registry.getMatchingRegistrations("unknown", "transfer.completed")).toHaveLength(0);
  });
});

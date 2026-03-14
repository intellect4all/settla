import type { WebhookRegistration } from "./types.js";

/**
 * In-memory webhook registration store.
 * Per-tenant: each tenant can have multiple webhook registrations.
 */
export class WebhookRegistry {
  /** webhookId → registration */
  private registrations = new Map<string, WebhookRegistration>();
  /** tenantId → Set<webhookId> */
  private byTenant = new Map<string, Set<string>>();

  register(reg: WebhookRegistration): void {
    this.registrations.set(reg.id, reg);
    let tenantSet = this.byTenant.get(reg.tenantId);
    if (!tenantSet) {
      tenantSet = new Set();
      this.byTenant.set(reg.tenantId, tenantSet);
    }
    tenantSet.add(reg.id);
  }

  unregister(webhookId: string): boolean {
    const reg = this.registrations.get(webhookId);
    if (!reg) return false;
    this.registrations.delete(webhookId);
    const tenantSet = this.byTenant.get(reg.tenantId);
    if (tenantSet) {
      tenantSet.delete(webhookId);
      if (tenantSet.size === 0) this.byTenant.delete(reg.tenantId);
    }
    return true;
  }

  /**
   * Get all active registrations for a tenant that match the event type.
   */
  getMatchingRegistrations(
    tenantId: string,
    eventType: string
  ): WebhookRegistration[] {
    const tenantSet = this.byTenant.get(tenantId);
    if (!tenantSet) return [];
    const result: WebhookRegistration[] = [];
    for (const id of tenantSet) {
      const reg = this.registrations.get(id);
      if (!reg || !reg.isActive) continue;
      // Empty events array means subscribe to all events
      if (reg.events.length === 0 || reg.events.includes(eventType)) {
        result.push(reg);
      }
    }
    return result;
  }

  getById(webhookId: string): WebhookRegistration | undefined {
    return this.registrations.get(webhookId);
  }

  getAll(): WebhookRegistration[] {
    return Array.from(this.registrations.values());
  }
}

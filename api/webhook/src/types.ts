export interface WebhookEvent {
  event: string;
  event_id: string;
  timestamp: string;
  data: Record<string, unknown>;
}

export interface WebhookRegistration {
  id: string;
  tenantId: string;
  url: string;
  secret: string;
  events: string[];
  isActive: boolean;
}

export interface DeliveryResult {
  webhookId: string;
  eventId: string;
  attempt: number;
  statusCode: number | null;
  success: boolean;
  durationMs: number;
  error?: string;
  retryAfterMs?: number;
}

export interface DeliveryTask {
  registration: WebhookRegistration;
  event: WebhookEvent;
  attempt: number;
}

export interface NatsEvent {
  ID: string;
  TenantID: string;
  Type: string;
  Timestamp: string;
  Data: Record<string, unknown>;
}

export interface DeadLetterEntry {
  eventId: string;
  webhookId: string;
  tenantId: string;
  event: WebhookEvent;
  lastAttempt: number;
  lastError: string;
  deadLetteredAt: string;
}

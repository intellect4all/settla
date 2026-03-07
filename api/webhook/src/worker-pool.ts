import type { Config } from "./config.js";
import type { Logger } from "./logger.js";
import type { DeliveryTask, DeadLetterEntry } from "./types.js";
import { deliverWebhook, shouldRetry, getRetryDelay } from "./delivery.js";

/**
 * Bounded worker pool for concurrent webhook deliveries.
 * Tasks are enqueued and processed by up to `poolSize` workers.
 * Retries are scheduled with exponential backoff delays.
 */
export class WorkerPool {
  private active = 0;
  private queue: DeliveryTask[] = [];
  private running = true;
  private pendingTimers = new Set<ReturnType<typeof setTimeout>>();
  private stats = {
    delivered: 0,
    failed: 0,
    retried: 0,
    deadLettered: 0,
  };

  constructor(
    private config: Config,
    private logger: Logger,
    private onDeadLetter?: (entry: DeadLetterEntry) => void
  ) {}

  enqueue(task: DeliveryTask): void {
    if (!this.running) return;
    this.queue.push(task);
    this.drain();
  }

  private drain(): void {
    while (
      this.running &&
      this.active < this.config.workerPoolSize &&
      this.queue.length > 0
    ) {
      const task = this.queue.shift()!;
      this.active++;
      this.process(task).finally(() => {
        this.active--;
        this.drain();
      });
    }
  }

  private async process(task: DeliveryTask): Promise<void> {
    const result = await deliverWebhook(task, this.config, this.logger);

    if (result.success) {
      this.stats.delivered++;
      return;
    }

    if (shouldRetry(result, this.config.maxRetries)) {
      this.stats.retried++;
      const delay = getRetryDelay(result, this.config.retryDelaysMs);
      const retryTask: DeliveryTask = {
        ...task,
        attempt: task.attempt + 1,
      };
      if (delay <= 0) {
        this.enqueue(retryTask);
      } else {
        const timer = setTimeout(() => {
          this.pendingTimers.delete(timer);
          this.enqueue(retryTask);
        }, delay);
        this.pendingTimers.add(timer);
      }
    } else {
      this.stats.failed++;
      if (task.attempt >= this.config.maxRetries) {
        this.stats.deadLettered++;
        const entry: DeadLetterEntry = {
          eventId: task.event.event_id,
          webhookId: task.registration.id,
          tenantId: task.registration.tenantId,
          event: task.event,
          lastAttempt: task.attempt,
          lastError: result.error || `HTTP ${result.statusCode}`,
          deadLetteredAt: new Date().toISOString(),
        };
        this.logger.deadLetter(entry);
        this.onDeadLetter?.(entry);
      }
    }
  }

  getStats() {
    return { ...this.stats, active: this.active, queued: this.queue.length };
  }

  async shutdown(): Promise<void> {
    this.running = false;
    this.queue = [];
    for (const timer of this.pendingTimers) {
      clearTimeout(timer);
    }
    this.pendingTimers.clear();
    // Wait for in-flight deliveries to complete (max 5s)
    const deadline = Date.now() + 5000;
    while (this.active > 0 && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
}

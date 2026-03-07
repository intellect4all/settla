import {
  connect,
  type NatsConnection,
  type JetStreamClient,
  type ConsumerMessages,
  type JetStreamManager,
  StringCodec,
} from "nats";
import type { Config } from "./config.js";
import type { Logger } from "./logger.js";
import type { NatsEvent } from "./types.js";
import { WebhookRegistry } from "./registry.js";
import { WorkerPool } from "./worker-pool.js";
import { buildWebhookEvent } from "./delivery.js";

const sc = StringCodec();

/**
 * NATS JetStream consumer that subscribes to transfer events
 * across all partitions and dispatches to the webhook worker pool.
 */
export class WebhookConsumer {
  private nc: NatsConnection | null = null;
  private js: JetStreamClient | null = null;
  private subscriptions: ConsumerMessages[] = [];

  constructor(
    private config: Config,
    private registry: WebhookRegistry,
    private pool: WorkerPool,
    private logger: Logger
  ) {}

  async connect(): Promise<void> {
    this.nc = await connect({
      servers: this.config.natsUrl,
      name: "settla-webhook-dispatcher",
      reconnect: true,
      maxReconnectAttempts: -1,
      reconnectTimeWait: 2000,
    });
    this.js = this.nc.jetstream();
    this.logger.info("connected to NATS", { url: this.config.natsUrl });
  }

  async subscribe(): Promise<void> {
    if (!this.nc || !this.js) {
      throw new Error("not connected to NATS");
    }

    const jsm: JetStreamManager = await this.nc.jetstreamManager();

    for (let partition = 0; partition < this.config.numPartitions; partition++) {
      const consumerName = `webhook-dispatcher-${partition}`;
      const filterSubject = `${this.config.subjectPrefix}.partition.${partition}.>`;

      // Ensure consumer exists (create or update)
      await jsm.consumers.add(this.config.streamName, {
        durable_name: consumerName,
        filter_subject: filterSubject,
        ack_policy: "explicit" as never,
        ack_wait: 30_000_000_000, // 30s in nanoseconds
        max_deliver: this.config.maxRetries + 1,
        deliver_policy: "all" as never,
      });

      const consumer = await this.js.consumers.get(
        this.config.streamName,
        consumerName
      );
      const messages = await consumer.consume();
      this.subscriptions.push(messages);

      this.logger.info("subscribed to partition", {
        partition,
        consumer: consumerName,
        filter: filterSubject,
      });

      // Process messages asynchronously
      this.processMessages(messages, partition);
    }
  }

  private async processMessages(
    messages: ConsumerMessages,
    partition: number
  ): Promise<void> {
    for await (const msg of messages) {
      try {
        const raw = sc.decode(msg.data);
        const natsEvent: NatsEvent = JSON.parse(raw);

        const registrations = this.registry.getMatchingRegistrations(
          natsEvent.TenantID,
          natsEvent.Type
        );

        if (registrations.length === 0) {
          msg.ack();
          continue;
        }

        const webhookEvent = buildWebhookEvent(
          natsEvent.ID,
          natsEvent.Type,
          natsEvent.Timestamp,
          natsEvent.Data
        );

        for (const reg of registrations) {
          this.pool.enqueue({
            registration: reg,
            event: webhookEvent,
            attempt: 1,
          });
        }

        // Ack the NATS message — webhook retries are handled by the worker pool
        msg.ack();
      } catch (err) {
        this.logger.error("failed to process NATS message", {
          partition,
          error: err instanceof Error ? err.message : String(err),
        });
        msg.nak();
      }
    }
  }

  async disconnect(): Promise<void> {
    for (const sub of this.subscriptions) {
      sub.stop();
    }
    this.subscriptions = [];
    if (this.nc) {
      await this.nc.drain();
      this.nc = null;
    }
    this.logger.info("disconnected from NATS");
  }
}

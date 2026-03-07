import * as grpc from "@grpc/grpc-js";

/**
 * GrpcPool maintains a pool of persistent gRPC channels to settla-server.
 * Round-robin selection per request. Unhealthy connections are replaced.
 *
 * Why: per-request TCP connections add ~1-3ms overhead. At 5,000 TPS that's
 * unacceptable. A persistent pool of ~50 connections amortises this to near zero.
 */
export class GrpcPool {
  private channels: grpc.Channel[] = [];
  private target: string;
  private poolSize: number;
  private index = 0;
  private credentials: grpc.ChannelCredentials;

  constructor(target: string, poolSize: number) {
    this.target = target;
    this.poolSize = poolSize;
    this.credentials = grpc.credentials.createInsecure();
  }

  /** Initialise all channels in the pool. */
  start(): void {
    for (let i = 0; i < this.poolSize; i++) {
      this.channels.push(this.createChannel());
    }
  }

  /** Round-robin next healthy channel. */
  getChannel(): grpc.Channel {
    if (this.channels.length === 0) {
      throw new Error("settla-gateway: gRPC pool not started");
    }

    // Try up to poolSize times to find a ready/idle channel
    for (let attempt = 0; attempt < this.channels.length; attempt++) {
      const idx = this.index % this.channels.length;
      this.index++;
      const ch = this.channels[idx];
      const state = ch.getConnectivityState(true);

      if (
        state === grpc.connectivityState.READY ||
        state === grpc.connectivityState.IDLE
      ) {
        return ch;
      }

      // Replace unhealthy channel
      if (state === grpc.connectivityState.SHUTDOWN) {
        ch.close();
        this.channels[idx] = this.createChannel();
        return this.channels[idx];
      }
    }

    // All channels busy/connecting — return next anyway (gRPC will queue)
    const idx = this.index % this.channels.length;
    this.index++;
    return this.channels[idx];
  }

  /** Close all channels. */
  async close(): Promise<void> {
    for (const ch of this.channels) {
      ch.close();
    }
    this.channels = [];
  }

  get size(): number {
    return this.channels.length;
  }

  private createChannel(): grpc.Channel {
    return new grpc.Channel(this.target, this.credentials, {
      // Keep alive to detect dead connections quickly
      "grpc.keepalive_time_ms": 10_000,
      "grpc.keepalive_timeout_ms": 5_000,
      "grpc.keepalive_permit_without_calls": 1,
      // Allow large messages for batch responses
      "grpc.max_receive_message_length": 16 * 1024 * 1024,
    });
  }
}

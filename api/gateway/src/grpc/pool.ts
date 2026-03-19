import * as grpc from "@grpc/grpc-js";

export type CircuitBreakerState = "closed" | "open" | "half-open";

/**
 * GrpcPool maintains a pool of persistent gRPC channels to settla-server.
 * Round-robin selection per request. Unhealthy connections are replaced.
 *
 * Includes a circuit breaker: after `failureThreshold` consecutive failures,
 * the pool rejects new requests for `cooldownMs`, then probes with 1 request.
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

  // In-flight request tracking for graceful drain
  private _inFlight = 0;
  private _draining = false;
  private _drainResolve: (() => void) | null = null;

  // Circuit breaker state
  private cbState: CircuitBreakerState = "closed";
  private consecutiveFailures = 0;
  private failureThreshold: number;
  private cooldownMs: number;
  private lastFailureTime = 0;
  private probing = false;
  private halfOpenSuccesses = 0;
  private halfOpenThreshold: number;

  constructor(target: string, poolSize: number, failureThreshold = 5, cooldownMs = 10_000, halfOpenThreshold = 3) {
    this.target = target;
    this.poolSize = poolSize;
    this.credentials = grpc.credentials.createInsecure();
    this.failureThreshold = failureThreshold;
    this.cooldownMs = cooldownMs;
    this.halfOpenThreshold = halfOpenThreshold;
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

  /** Track the start of an in-flight gRPC call. */
  trackStart(): void {
    this._inFlight++;
  }

  /** Track the end of an in-flight gRPC call. Resolves drain if complete. */
  trackEnd(): void {
    this._inFlight = Math.max(0, this._inFlight - 1);
    if (this._draining && this._inFlight <= 0 && this._drainResolve) {
      this._drainResolve();
    }
  }

  /** Number of in-flight gRPC requests. */
  get inFlight(): number {
    return this._inFlight;
  }

  /**
   * Gracefully drain: wait for all in-flight requests to complete (up to timeoutMs),
   * then close all channels. Call this before close() during shutdown.
   */
  async drain(timeoutMs = 10_000): Promise<void> {
    this._draining = true;
    if (this._inFlight <= 0) {
      return;
    }
    await Promise.race([
      new Promise<void>((resolve) => {
        this._drainResolve = resolve;
      }),
      new Promise<void>((resolve) => setTimeout(resolve, timeoutMs)),
    ]);
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

  /** Returns true if the circuit breaker is open (rejecting requests). */
  isOpen(): boolean {
    if (this.cbState === "open") {
      // Check if cooldown has elapsed → transition to half-open
      if (Date.now() - this.lastFailureTime >= this.cooldownMs) {
        // Only allow one concurrent probe request in half-open state.
        // Without this flag, multiple requests could slip through simultaneously
        // during the half-open window, all hitting the (potentially still failing)
        // backend and causing unnecessary load.
        if (this.probing) {
          return true; // Another request is already probing
        }
        this.probing = true;
        this.cbState = "half-open";
        return false;
      }
      return true;
    }
    return false;
  }

  /** Current circuit breaker state. */
  get circuitState(): CircuitBreakerState {
    // Refresh state (cooldown check)
    if (this.cbState === "open" && Date.now() - this.lastFailureTime >= this.cooldownMs) {
      this.cbState = "half-open";
    }
    return this.cbState;
  }

  /** Record a successful gRPC call. Requires multiple successes in half-open before closing. */
  recordSuccess(): void {
    if (this.cbState === "half-open") {
      this.halfOpenSuccesses++;
      this.probing = false; // allow next probe
      if (this.halfOpenSuccesses >= this.halfOpenThreshold) {
        this.consecutiveFailures = 0;
        this.cbState = "closed";
        this.halfOpenSuccesses = 0;
      }
      return;
    }
    this.consecutiveFailures = 0;
  }

  /** Record a failed gRPC call. Opens the circuit after threshold consecutive failures. */
  recordFailure(): void {
    // In half-open state, any failure immediately re-opens the circuit
    if (this.cbState === "half-open") {
      this.halfOpenSuccesses = 0;
      this.cbState = "open";
      this.lastFailureTime = Date.now();
      this.probing = false;
      return;
    }
    this.consecutiveFailures++;
    this.lastFailureTime = Date.now();
    this.probing = false;
    if (this.consecutiveFailures >= this.failureThreshold) {
      this.cbState = "open";
    }
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

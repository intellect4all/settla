export interface Config {
  port: number;
  host: string;
  natsUrl: string;
  streamName: string;
  subjectPrefix: string;
  numPartitions: number;
  workerPoolSize: number;
  deliveryTimeoutMs: number;
  maxRetries: number;
  retryDelaysMs: number[];
}

export function loadConfig(): Config {
  return {
    port: Number(process.env.PORT) || 3001,
    host: process.env.HOST || "0.0.0.0",
    natsUrl: process.env.NATS_URL || "nats://localhost:4222",
    streamName: process.env.NATS_STREAM || "SETTLA_TRANSFERS",
    subjectPrefix: process.env.NATS_SUBJECT_PREFIX || "settla.transfer",
    numPartitions: Number(process.env.NUM_PARTITIONS) || 8,
    workerPoolSize: Number(process.env.WORKER_POOL_SIZE) || 20,
    deliveryTimeoutMs: Number(process.env.DELIVERY_TIMEOUT_MS) || 30_000,
    maxRetries: Number(process.env.MAX_RETRIES) || 5,
    retryDelaysMs: [0, 30_000, 120_000, 900_000, 3_600_000],
  };
}

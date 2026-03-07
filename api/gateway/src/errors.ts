import * as grpc from "@grpc/grpc-js";
import type { FastifyReply } from "fastify";

/** Map gRPC status codes to HTTP status codes and send error response. */
export function mapGrpcError(reply: FastifyReply, err: unknown): void {
  const grpcErr = err as grpc.ServiceError;

  if (!grpcErr || !grpcErr.code) {
    reply.status(500).send({
      error: "INTERNAL",
      message: "An unexpected error occurred",
    });
    return;
  }

  const { code, details } = grpcErr;
  const message = details || "An error occurred";

  switch (code) {
    case grpc.status.NOT_FOUND:
      reply.status(404).send({ error: "NOT_FOUND", message });
      break;
    case grpc.status.ALREADY_EXISTS:
      reply.status(409).send({ error: "CONFLICT", message });
      break;
    case grpc.status.INVALID_ARGUMENT:
      reply.status(400).send({ error: "BAD_REQUEST", message });
      break;
    case grpc.status.FAILED_PRECONDITION:
      reply.status(422).send({ error: "UNPROCESSABLE", message });
      break;
    case grpc.status.PERMISSION_DENIED:
      reply.status(403).send({ error: "FORBIDDEN", message });
      break;
    case grpc.status.UNAUTHENTICATED:
      reply.status(401).send({ error: "UNAUTHORIZED", message });
      break;
    case grpc.status.RESOURCE_EXHAUSTED:
      reply.status(429).send({ error: "RATE_LIMITED", message });
      break;
    case grpc.status.UNAVAILABLE:
      reply.status(503).send({ error: "UNAVAILABLE", message });
      break;
    case grpc.status.DEADLINE_EXCEEDED:
      reply.status(504).send({ error: "TIMEOUT", message });
      break;
    default:
      reply.status(500).send({ error: "INTERNAL", message });
  }
}

import * as grpc from "@grpc/grpc-js";
import type { FastifyReply, FastifyRequest } from "fastify";

/** Map gRPC status codes to HTTP status codes and send error response. */
export function mapGrpcError(request: FastifyRequest, reply: FastifyReply, err: unknown): FastifyReply {
  const grpcErr = err as grpc.ServiceError;
  const request_id = request.id;

  if (!grpcErr || !grpcErr.code) {
    return reply.status(500).send({
      error: "INTERNAL",
      message: "An unexpected error occurred",
      request_id,
    });
  }

  const { code, details } = grpcErr;
  const message = details || "An error occurred";

  switch (code) {
    case grpc.status.NOT_FOUND:
      return reply.status(404).send({ error: "NOT_FOUND", message, request_id });
    case grpc.status.ALREADY_EXISTS:
      return reply.status(409).send({ error: "CONFLICT", message, request_id });
    case grpc.status.INVALID_ARGUMENT:
      return reply.status(400).send({ error: "BAD_REQUEST", message, request_id });
    case grpc.status.FAILED_PRECONDITION:
      return reply.status(422).send({ error: "UNPROCESSABLE", message, request_id });
    case grpc.status.PERMISSION_DENIED:
      return reply.status(403).send({ error: "FORBIDDEN", message, request_id });
    case grpc.status.UNAUTHENTICATED:
      return reply.status(401).send({ error: "UNAUTHORIZED", message, request_id });
    case grpc.status.RESOURCE_EXHAUSTED:
      return reply.status(429).send({ error: "RATE_LIMITED", message, request_id });
    case grpc.status.UNAVAILABLE:
      return reply.status(503).send({ error: "UNAVAILABLE", message, request_id });
    case grpc.status.DEADLINE_EXCEEDED:
      return reply.status(504).send({ error: "TIMEOUT", message, request_id });
    default:
      return reply.status(500).send({ error: "INTERNAL", message, request_id });
  }
}

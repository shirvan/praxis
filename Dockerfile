# Unified multi-target Dockerfile for all Praxis services.
#
# A single build stage compiles every binary, sharing the module download and
# Go build cache. Each service gets its own tiny final stage that docker-compose
# selects via `target:`. This lets `docker compose up --build` run in parallel
# without redundant Go compilations fighting for CPU.

# ── Shared build stage ──────────────────────────────────────────────────────

FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /out/praxis-core          ./cmd/praxis-core
RUN CGO_ENABLED=0 go build -o /out/praxis-storage        ./cmd/praxis-storage
RUN CGO_ENABLED=0 go build -o /out/praxis-network        ./cmd/praxis-network
RUN CGO_ENABLED=0 go build -o /out/praxis-compute        ./cmd/praxis-compute
RUN CGO_ENABLED=0 go build -o /out/praxis-identity       ./cmd/praxis-identity
RUN CGO_ENABLED=0 go build -o /out/praxis-monitoring     ./cmd/praxis-monitoring

# ── Per-service runtime stages ──────────────────────────────────────────────

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
ARG PRAXIS_VERSION=alpha
ARG PRAXIS_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/shirvan/praxis" \
      org.opencontainers.image.version="${PRAXIS_VERSION}" \
      org.opencontainers.image.revision="${PRAXIS_REVISION}" \
      org.opencontainers.image.licenses="Apache-2.0"

FROM runtime AS praxis-core
COPY --from=builder /out/praxis-core /praxis-core
COPY schemas/ /schemas/
ENTRYPOINT ["/praxis-core"]

FROM runtime AS praxis-storage
COPY --from=builder /out/praxis-storage /praxis-storage
ENTRYPOINT ["/praxis-storage"]

FROM runtime AS praxis-network
COPY --from=builder /out/praxis-network /praxis-network
ENTRYPOINT ["/praxis-network"]

FROM runtime AS praxis-compute
COPY --from=builder /out/praxis-compute /praxis-compute
ENTRYPOINT ["/praxis-compute"]

FROM runtime AS praxis-identity
COPY --from=builder /out/praxis-identity /praxis-identity
ENTRYPOINT ["/praxis-identity"]

FROM runtime AS praxis-monitoring
COPY --from=builder /out/praxis-monitoring /praxis-monitoring
ENTRYPOINT ["/praxis-monitoring"]

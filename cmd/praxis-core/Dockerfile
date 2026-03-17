# Runtime image for Praxis Core.
#
# The build stays intentionally conventional so both humans and agents can
# inspect it quickly:
# 1. restore modules
# 2. copy the repository
# 3. build the single static binary
# 4. ship only the binary plus the schema bundle needed at runtime

FROM golang:1.25 AS builder

WORKDIR /app

# Copy module metadata first so Docker can reuse the dependency-download layer
# when source files change but dependencies do not.
COPY go.mod go.sum ./
RUN go mod download

# Copy the full repository because praxis-core depends on internal packages and
# the schemas directory is also part of the runtime contract.
COPY . .

# Praxis Core is a pure Go binary, so a CGO-free build produces a small,
# portable artifact that runs cleanly in a distroless image.
RUN CGO_ENABLED=0 go build -o /praxis-core ./cmd/praxis-core

FROM gcr.io/distroless/static-debian12:nonroot

# The runtime only needs the compiled binary and the schema bundle used by the
# template engine. Keeping the final image small reduces startup time and makes
# the container contents easy to reason about.
COPY --from=builder /praxis-core /praxis-core
COPY schemas/ /schemas/

ENTRYPOINT ["/praxis-core"]

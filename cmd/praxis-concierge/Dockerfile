FROM golang:1.25 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /praxis-concierge ./cmd/praxis-concierge

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /praxis-concierge /praxis-concierge

ENTRYPOINT ["/praxis-concierge"]

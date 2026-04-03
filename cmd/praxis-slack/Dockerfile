FROM golang:1.25 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /praxis-slack ./cmd/praxis-slack

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /praxis-slack /praxis-slack

ENTRYPOINT ["/praxis-slack"]

# Runtime image for Praxis Notifications.

FROM golang:1.25 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /praxis-notifications ./cmd/praxis-notifications

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /praxis-notifications /praxis-notifications
COPY schemas/ /schemas/

ENTRYPOINT ["/praxis-notifications"]
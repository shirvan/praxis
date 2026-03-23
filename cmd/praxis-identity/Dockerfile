FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-identity ./cmd/praxis-identity

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-identity /praxis-identity
ENTRYPOINT ["/praxis-identity"]
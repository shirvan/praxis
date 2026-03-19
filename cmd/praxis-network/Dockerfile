FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-network ./cmd/praxis-network

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-network /praxis-network
ENTRYPOINT ["/praxis-network"]

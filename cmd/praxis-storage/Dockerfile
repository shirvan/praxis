FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-storage ./cmd/praxis-storage

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-storage /praxis-storage
ENTRYPOINT ["/praxis-storage"]

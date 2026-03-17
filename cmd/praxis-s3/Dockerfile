FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-s3 ./cmd/praxis-s3

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-s3 /praxis-s3
ENTRYPOINT ["/praxis-s3"]

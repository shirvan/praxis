FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-monitoring ./cmd/praxis-monitoring

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-monitoring /praxis-monitoring
ENTRYPOINT ["/praxis-monitoring"]
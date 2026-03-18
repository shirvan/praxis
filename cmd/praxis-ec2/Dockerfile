FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-ec2 ./cmd/praxis-ec2

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-ec2 /praxis-ec2
ENTRYPOINT ["/praxis-ec2"]
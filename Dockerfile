FROM golang:1.22-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

RUN apt-get update -q && apt-get install -y -q gcc

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -o /mate-relay ./cmd/mate-relay

FROM debian:bookworm-slim

RUN apt-get update -q && apt-get install -y -q ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /mate-relay /mate-relay

EXPOSE 80 443

ENTRYPOINT ["/mate-relay"]

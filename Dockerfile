FROM golang:1.27-alpine3.24 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o agent .

FROM alpine:3.22

WORKDIR /app
COPY --from=builder /app/agent .

EXPOSE 27973/tcp
ENTRYPOINT ["/app/agent", "--config", "/app/config/agent.yml"]

FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o agentops ./cmd/api

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/agentops .
EXPOSE 8080
CMD ["./agentops"]

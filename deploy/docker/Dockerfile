# syntax=docker/dockerfile:1.4
# Stage 1: Build binary
FROM golang:1.24-alpine AS builder

WORKDIR /build

RUN apk add --no-cache ca-certificates tzdata git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Compile binary statically
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-w -s -extldflags '-static'" \
    -o /build/mycaffe-api ./cmd/api

# Stage 2: Minimal runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata curl \
    && adduser -D -u 10001 mycaffe

WORKDIR /app

COPY --from=builder /build/mycaffe-api /app/mycaffe-api

# Change ownership
USER mycaffe:mycaffe

EXPOSE 8080

HEALTHCHECK --interval=20s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/api/v1/health || exit 1

ENTRYPOINT ["/app/mycaffe-api"]

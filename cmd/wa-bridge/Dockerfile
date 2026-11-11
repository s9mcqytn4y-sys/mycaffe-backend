# syntax=docker/dockerfile:1.4
# wa-bridge (decision W12): demo/development WhatsApp relay via whatsmeow. Pure Go (modernc SQLite), no cgo.
FROM golang:1.27.4-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-w -s" -o /build/wa-bridge .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10002 wabridge && install -d -o wabridge -g wabridge /data
COPY --from=builder /build/wa-bridge /app/wa-bridge
USER wabridge:wabridge
VOLUME ["/data"]
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s CMD wget -qO- http://127.0.0.1:8090/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/wa-bridge"]

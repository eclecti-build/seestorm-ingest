FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /seestorm-ingest ./cmd/ingest

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /seestorm-ingest /usr/local/bin/seestorm-ingest
CMD ["seestorm-ingest"]

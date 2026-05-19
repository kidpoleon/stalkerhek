FROM golang:1.24-alpine AS build

WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/stalkerhek ./cmd/stalkerhek

FROM alpine:3.20

RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    ffmpeg \
    curl

WORKDIR /app

COPY --from=build /out/stalkerhek /app/stalkerhek
COPY graphic/ /app/graphic/

ENV STALKERHEK_ROOT=/app

# Web UI; HLS/Proxy ports are chosen per profile.
EXPOSE 4400

HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
  CMD curl -fsS http://127.0.0.1:4400/health || exit 1

ENTRYPOINT ["/app/stalkerhek"]

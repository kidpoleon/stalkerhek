FROM golang:1.21-alpine AS build

WORKDIR /src

# Needed for fetching modules
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/stalkerhek ./cmd/stalkerhek

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=build /out/stalkerhek /app/stalkerhek

# profiles.json is read/written relative to WORKDIR (/app)
EXPOSE 4400

ENTRYPOINT ["/app/stalkerhek"]

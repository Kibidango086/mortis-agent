# ============================================================
# Stage 1: Build the mortis-agent binary
# ============================================================
FROM golang:1.26.0-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN make build

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata curl

# Copy binary
COPY --from=builder /src/build/mortis-agent /usr/local/bin/mortis-agent

# Create mortis-agent home directory
RUN /usr/local/bin/mortis-agent onboard

ENTRYPOINT ["mortis-agent"]
CMD ["gateway"]

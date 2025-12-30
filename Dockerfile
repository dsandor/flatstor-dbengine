# Build stage
FROM golang:1.24-bookworm AS builder

# Install build dependencies for DuckDB
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with CGO enabled (required for DuckDB)
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /dbengine ./cmd/dbengine

# Runtime stage - use slim debian for smaller image with glibc support
FROM debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user for security
RUN useradd -r -u 1000 -m dbengine

# Create data directory
RUN mkdir -p /data && chown dbengine:dbengine /data

WORKDIR /app

# Copy binary from builder
COPY --from=builder /dbengine /app/dbengine

# Set ownership
RUN chown -R dbengine:dbengine /app

# Switch to non-root user
USER dbengine

# Default environment variables
ENV DATA_PATH=/data
ENV DUCKDB_PATH=""
ENV API_ADDR=:8080

# Expose API port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Default command: run API server
ENTRYPOINT ["/app/dbengine"]
CMD ["-api", "-data", "/data", "-api-addr", ":8080"]

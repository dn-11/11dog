# Stage 1: Build ITDog-style Vue 3 frontend
FROM --platform=$BUILDPLATFORM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build 11dog-server and self-hosted Linux agent binaries
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-builder /src/web/dist ./web/dist
ARG VERSION=1.0.0
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X github.com/dn-11/11dog/internal/server.Version=${VERSION}" \
    -o /out/11dog-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X github.com/dn-11/11dog/internal/agent.Version=${VERSION}" \
    -o /out/downloads/11dog-agent-linux-amd64 ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
    -ldflags="-s -w -X github.com/dn-11/11dog/internal/agent.Version=${VERSION}" \
    -o /out/downloads/11dog-agent-linux-arm64 ./cmd/agent

# Stage 3: Minimal runtime image
FROM alpine:3.21
LABEL org.opencontainers.image.source="https://github.com/dn-11/11dog"
LABEL org.opencontainers.image.description="11Dog - DN11 Network Diagnostic Platform (Server + Hosted Agent Binaries)"
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 10001 dog && mkdir -p /var/lib/11dog /opt/11dog/downloads && chown -R dog:dog /var/lib/11dog /opt/11dog
COPY --from=go-builder /out/11dog-server /usr/local/bin/11dog-server
COPY --from=go-builder /out/downloads /opt/11dog/downloads
ENV DOG_AGENT_BIN_DIR=/opt/11dog/downloads
USER dog
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/11dog-server"]

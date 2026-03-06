# Dockerfile — Combined protocol + marketplace image (single-binary deployment).
#
# This is the default image for simple deployments where both the protocol
# layer and the marketplace application run in the same container. The
# protocol node starts with the --marketplace flag which enables the built-in
# marketplace (tasks, escrow, routing, explorer) alongside protocol endpoints.
#
# For a split deployment (protocol node + separate marketplace), use
# Dockerfile.node and Dockerfile.marketplace instead, together with the
# two-service docker-compose configuration in docker-compose.split.yml.

# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /aethernet ./cmd/node
RUN CGO_ENABLED=0 go build -o /aet ./cmd/aet
RUN CGO_ENABLED=0 go build -o /marketplace ./cmd/marketplace

# Run stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /aethernet /usr/local/bin/aethernet
COPY --from=builder /aet /usr/local/bin/aet
COPY --from=builder /marketplace /usr/local/bin/marketplace
COPY explorer/ /usr/local/share/aethernet/explorer/
EXPOSE 8337 8338 8340
VOLUME /data
ENV AETHERNET_DATA=/data
ENTRYPOINT ["aethernet"]
CMD ["start", "--marketplace"]

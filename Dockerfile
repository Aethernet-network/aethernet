# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /aethernet ./cmd/node
RUN CGO_ENABLED=0 go build -o /aet ./cmd/aet

# Run stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /aethernet /usr/local/bin/aethernet
COPY --from=builder /aet /usr/local/bin/aet
COPY explorer/ /usr/local/share/aethernet/explorer/
EXPOSE 8337 8338
VOLUME /data
ENV AETHERNET_DATA=/data
ENTRYPOINT ["aethernet"]
CMD ["start"]

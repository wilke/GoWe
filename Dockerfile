# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gowe-server ./cmd/server

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache docker-cli
WORKDIR /app
COPY --from=builder /gowe-server /usr/local/bin/gowe-server
COPY ui/assets ui/assets
EXPOSE 8080
ENTRYPOINT ["gowe-server"]

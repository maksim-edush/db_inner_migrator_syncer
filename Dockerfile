# Build the migrator binary
FROM golang:1.21 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go mod download
RUN go build -o /out/migrator ./cmd/migrator

# Slim runtime image
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/migrator /usr/local/bin/migrator
WORKDIR /workspace
ENTRYPOINT ["migrator"]

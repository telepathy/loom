# 多阶段构建 Loom DAS 服务镜像
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=v0.0.0-dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o loom-server .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/loom-server /usr/local/bin/loom-server
EXPOSE 8080
ENTRYPOINT ["loom-server"]

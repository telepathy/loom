.PHONY: build run vendor clean test vet

# Version (override with make VERSION=v0.0.4)
VERSION ?= dev

# Build
build:
	CGO_ENABLED=0 go build -mod=vendor -ldflags="-X main.version=$(VERSION)" -o loom-server .

# Run locally with MySQL + local executor
run: build
	DAS_PORT=8080 \
	DAS_LOCAL_MODE=true \
	DAS_MYSQL_DSN="root:loom123@tcp(127.0.0.1:3307)/loom?charset=utf8mb4&parseTime=True&loc=Local" \
	./loom-server

# Run locally without MySQL
run-dev: build
	DAS_PORT=8080 \
	DAS_LOCAL_MODE=true \
	./loom-server

# Update vendor
vendor:
	go mod tidy
	go mod vendor

# Run tests
test:
	go test -mod=vendor ./...

# Run vet
vet:
	go vet -mod=vendor ./...

# Clean build artifacts
clean:
	rm -f loom-server

# Docker build
docker:
	docker build --build-arg VERSION=$(VERSION) -t registry/gps-das:$(VERSION) .

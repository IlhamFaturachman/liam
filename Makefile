.PHONY: build build-all run clean test deps

BINARY=liam

# Build for current platform (no CGO needed thanks to modernc.org/sqlite)
build:
	go build -o bin/$(BINARY) ./cmd/liam

# Build for all platforms (pure Go, no cross-compiler needed)
build-all: build-darwin-arm64 build-darwin-amd64 build-linux-amd64 build-linux-arm64 build-windows-amd64

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -o bin/$(BINARY)-darwin-arm64 ./cmd/liam

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -o bin/$(BINARY)-darwin-amd64 ./cmd/liam

build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -o bin/$(BINARY)-linux-amd64 ./cmd/liam

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -o bin/$(BINARY)-linux-arm64 ./cmd/liam

build-windows-amd64:
	GOOS=windows GOARCH=amd64 go build -o bin/$(BINARY)-windows-amd64.exe ./cmd/liam

# Run locally
run:
	go run ./cmd/liam serve

# Test
test:
	go test ./...

# Clean
clean:
	rm -rf bin/

# Install dependencies
deps:
	go mod download
	go mod tidy

# Show binary info
info:
	@ls -lh bin/ 2>/dev/null || echo "No binaries built yet. Run 'make build' or 'make build-all'"

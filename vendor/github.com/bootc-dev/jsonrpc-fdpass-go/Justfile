# Format and lint Go code
check:
	go fmt ./...
	go vet ./...

# Run unit tests
unit:
	go test -v ./...

# Run unit tests with race detector
test-race:
	go test -race -v ./...

# Build all packages
build:
	go build ./...

# Build the example
build-example:
	go build -o target/echo ./examples/echo

# Run all tests
test-all: unit

# Clean build artifacts
clean:
	rm -rf target/
	rm -rf tests-integration/target/
	go clean ./...

# Full CI check (format, lint, test)
ci: check unit

# Run the integration tests against the Rust implementation
# Requires: cargo, go
test-integration: build-integration-server
	go test -v ./tests-integration/...

# Build the Rust integration test server
build-integration-server:
	cargo build --manifest-path tests-integration/Cargo.toml

# Run the echo server example
run-server socket="/tmp/echo.sock":
	go run ./examples/echo server {{socket}}

# Run the echo client example
run-client socket="/tmp/echo.sock":
	go run ./examples/echo client {{socket}}

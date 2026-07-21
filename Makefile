BINARY_NAME=mini-s3-server

.PHONY: all build run clean certs test test-cover

all: build

build:
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BINARY_NAME) .
	@echo "$(BINARY_NAME) built successfully."

run: build
	@echo "Starting $(BINARY_NAME)..."
	@./$(BINARY_NAME)

clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@echo "Cleanup complete."

test:
	@go test -v -count=1 -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

test-cover:
	@go test -v -count=1 -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

certs:
	@if [ ! -f certs/cert.pem ] || [ ! -f certs/key.pem ]; then \
		echo "Generating self-signed SSL certificates..."; \
		mkdir -p certs; \
		openssl req -x509 -newkey rsa:4096 -nodes -out certs/cert.pem -keyout certs/key.pem -days 365 -subj "/CN=localhost"; \
		echo "Certificates generated in certs/ directory."; \
	else \
		echo "Certificates already exist in certs/ directory."; \
	fi

# Ensure data directory exists (optional, as main.go also creates it)
data_dir:
	@mkdir -p data


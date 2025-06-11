BINARY_NAME=mini-s3-server
GO_FILES=main.go

.PHONY: all build run clean certs

all: build

build:
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BINARY_NAME) $(GO_FILES)
	@echo "$(BINARY_NAME) built successfully."

run: build
	@echo "Starting $(BINARY_NAME)..."
	@./$(BINARY_NAME)

clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@echo "Cleanup complete."

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


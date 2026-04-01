.PHONY: build test bench lint clean docker run fmt vet

BINARY=pggate
BUILD_DIR=bin
GO=go

build:
	$(GO) build -o $(BUILD_DIR)/$(BINARY) ./cmd/pggate

run: build
	$(BUILD_DIR)/$(BINARY)

test:
	$(GO) test ./... -v -count=1

bench:
	$(GO) test ./... -bench=. -benchmem -run=^$

lint: vet
	@which golangci-lint > /dev/null 2>&1 || echo "install golangci-lint: https://golangci-lint.run/usage/install/"
	golangci-lint run ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(BUILD_DIR)

docker:
	docker build -t pggate:latest .

docker-run: docker
	docker run --rm -p 5432:5432 -p 8080:8080 -v $(PWD)/config.yaml:/app/config.yaml pggate:latest

tidy:
	$(GO) mod tidy

check: fmt vet test
	@echo "All checks passed"

BINARY_NAME=ff
CMD_PATH=.

VERSION?=dev
BUILD_DIR=./bin

GO=go

ifeq ($(OS),Windows_NT)
	BINARY_EXT=.exe
else
	BINARY_EXT=
endif

.PHONY: all build run test clean fmt vet cue-check lint install tidy coverage-gate help

all: build

build:
	@echo "Building $(BINARY_NAME)$(BINARY_EXT)..."
	$(GO) build -ldflags "-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME)$(BINARY_EXT) $(CMD_PATH)

run:
	$(GO) run $(CMD_PATH)

test:
	$(GO) test ./...

coverage:
	$(GO) test ./... -coverprofile=coverage.out
	$(GO) tool cover -html=coverage.out

coverage-gate:
	@echo "Checking cmd coverage >=30%..."
	@$(GO) test ./cmd -coverprofile=coverage_cmd.out -covermode=count
	@pct=$$(go tool cover -func=coverage_cmd.out | awk '/total:/ {print $$3}' | tr -d '%'); \
	echo "cmd coverage: $${pct}%"; \
	awk -v pct="$$pct" 'BEGIN { if (pct+0 < 30) { print "FAIL: cmd coverage " pct "% is below 30% gate"; exit 1 } else { print "PASS: coverage gate passed" } }'

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

cue-check:
	cd cue && cue vet .
	cd cue && cue vet . testdata/valid-config.yaml -d "#Config" -c
	cd cue && cue vet . testdata/valid-minimal.yaml -d "#Config" -c
	cd cue && cue vet . testdata/valid-providers.yaml -d "#Config" -c

lint:
	golangci-lint run

clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out

tidy:
	$(GO) mod tidy

install:
	$(GO) install $(CMD_PATH)

release:
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)
	GOOS=windows GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_PATH)
	GOOS=darwin GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_PATH)
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)

help:
	@echo "Forcefield Make Commands:"
	@echo "  make build      Build binary"
	@echo "  make run        Run locally"
	@echo "  make test       Run tests"
	@echo "  make coverage   Generate coverage"
	@echo "  make fmt        Format code"
	@echo "  make vet        Run go vet"
	@echo "  make cue-check  Validate CUE config schemas"
	@echo "  make lint       Run linter"
	@echo "  make clean      Remove builds"
	@echo "  make tidy       Update modules"
	@echo "  make install    Install binary"
	@echo "  make release    Build releases"

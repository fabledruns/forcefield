BINARY_NAME=ff
CMD_PATH=.

VERSION?=dev
BUILD_DIR=./bin
LDFLAGS=-s -w -X forcefield/cmd.Version=$(VERSION) -X main.Version=$(VERSION)

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
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)$(BINARY_EXT) $(CMD_PATH)

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

ifeq ($(OS),Windows_NT)
release:
	@if not exist "$(BUILD_DIR)" mkdir "$(BUILD_DIR)"
	@set GOOS=linux&& set GOARCH=amd64&& $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)
	@set GOOS=linux&& set GOARCH=arm64&& $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)
	@set GOOS=windows&& set GOARCH=amd64&& $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_PATH)
	@set GOOS=windows&& set GOARCH=arm64&& $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-arm64.exe $(CMD_PATH)
	@set GOOS=darwin&& set GOARCH=amd64&& $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_PATH)
	@set GOOS=darwin&& set GOARCH=arm64&& $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)
	@python -c "import hashlib,glob,os; d=r'$(BUILD_DIR)'; files=sorted(glob.glob(os.path.join(d,'$(BINARY_NAME)-*'))); open(os.path.join(d,'checksums.txt'),'w').write(''.join(f'{hashlib.sha256(open(f,\"rb\").read()).hexdigest()}  {os.path.basename(f)}\n' for f in files))" 2>nul || python3 -c "import hashlib,glob,os; d=r'$(BUILD_DIR)'; files=sorted(glob.glob(os.path.join(d,'$(BINARY_NAME)-*'))); open(os.path.join(d,'checksums.txt'),'w').write(''.join(f'{hashlib.sha256(open(f,\"rb\").read()).hexdigest()}  {os.path.basename(f)}\n' for f in files))"
	@echo Release artifacts in $(BUILD_DIR):
	@type $(BUILD_DIR)\checksums.txt 2>nul || cat $(BUILD_DIR)/checksums.txt 2>nul || echo "checksums generated"
else
release:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_PATH)
	GOOS=windows GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-arm64.exe $(CMD_PATH)
	GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_PATH)
	GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)
	@python3 -c "import hashlib,glob,os; d='$(BUILD_DIR)'; files=sorted(glob.glob(os.path.join(d,'$(BINARY_NAME)-*'))); open(os.path.join(d,'checksums.txt'),'w').write(''.join(f'{hashlib.sha256(open(f,\"rb\").read()).hexdigest()}  {os.path.basename(f)}\n' for f in files))" 2>/dev/null || python -c "import hashlib,glob,os; d='$(BUILD_DIR)'; files=sorted(glob.glob(os.path.join(d,'$(BINARY_NAME)-*'))); open(os.path.join(d,'checksums.txt'),'w').write(''.join(f'{hashlib.sha256(open(f,\"rb\").read()).hexdigest()}  {os.path.basename(f)}\n' for f in files))" || (cd $(BUILD_DIR) && sha256sum $(BINARY_NAME)-* > checksums.txt 2>/dev/null || shasum -a 256 $(BINARY_NAME)-* > checksums.txt)
	@echo "Release artifacts in $(BUILD_DIR):"
	@cat $(BUILD_DIR)/checksums.txt
endif

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

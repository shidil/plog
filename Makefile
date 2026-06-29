BINARY := plog
PKG    := ./cmd/plog
BIN    := bin/$(BINARY)
SAMPLE := testdata/sample.log

# Reasonable defaults; override on the command line, e.g.
#   make test PKG_TEST=./internal/parse RUN=TestLine
#   make fuzz FUZZ=FuzzParseLine FUZZTIME=60s
PKG_TEST ?= ./...
RUN      ?=
FUZZ     ?= FuzzParseLine
FUZZTIME ?= 30s

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into bin/
	go build -o $(BIN) $(PKG)

.PHONY: run
run: ## Pipe the bundled sample through plog (ARGS="--no-fold" to pass flags)
	go run $(PKG) $(ARGS) < $(SAMPLE)

.PHONY: install
install: ## Install plog into $GOBIN / $GOPATH/bin
	go install $(PKG)

.PHONY: test
test: ## Run tests (PKG_TEST=./internal/parse RUN=TestLine to narrow)
	go test $(if $(RUN),-run $(RUN),) $(PKG_TEST)

.PHONY: race
race: ## Run tests with the race detector
	go test -race $(PKG_TEST)

.PHONY: fuzz
fuzz: ## Fuzz the parser (FUZZ=Name FUZZTIME=60s to adjust)
	go test -run=^$$ -fuzz=$(FUZZ) -fuzztime=$(FUZZTIME) ./internal/parse

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .

.PHONY: vet
vet: ## Report formatting drift and run go vet
	@gofmt -l . | grep . && { echo "gofmt: files need formatting (run 'make fmt')"; exit 1; } || true
	go vet ./...

.PHONY: check
check: vet test ## Vet then test — the pre-commit gate

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin
	go clean

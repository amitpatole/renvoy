GO ?= go
BIN := bin/renvoy

.PHONY: check fmt-check lint test build golden examples clean

## check: the gate CI runs (formatting, lint, race tests, examples)
check: fmt-check lint test examples

fmt-check:
	golangci-lint fmt --diff ./...

lint:
	golangci-lint run ./...

test:
	$(GO) test -race -count=1 ./...

build:
	$(GO) build -trimpath -o $(BIN) ./cmd/renvoy

## golden: rewrite golden files after an intentional rendering change
golden:
	$(GO) test ./internal/providers -run TestGolden -update

## examples: validate and dry-run every example for every provider
examples: build
	@set -e; for f in examples/*/renvoy.yaml; do \
		echo "== $$f"; \
		./$(BIN) validate -f $$f; \
		./$(BIN) generate -f $$f --all --dry-run >/dev/null; \
	done

clean:
	rm -rf bin

.PHONY: build test test-integration vet fmt-check clean tidy

BINARY := bin/bot
GO     ?= go

build:
	mkdir -p bin
	$(GO) build -o $(BINARY) ./cmd/bot

test:
	$(GO) test ./...

test-integration:
	$(GO) test -tags=integration ./...

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$out" ]; then echo "Files need gofmt:"; echo "$$out"; exit 1; fi

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin

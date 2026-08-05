GO ?= go
BINARIES := api scanner withdraw sweep sign

.PHONY: all build fmt vet test lint tidy migrate docker clean

all: fmt vet test build

build:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "building $$b"; \
		CGO_ENABLED=0 $(GO) build -trimpath -o bin/$$b ./cmd/$$b || exit 1; \
	done

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./... -count=1

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, ran go vet instead" && $(GO) vet ./...

tidy:
	$(GO) mod tidy

# Applies the schema through AutoMigrate. Production should apply
# deploy/migrations/*.sql instead so index changes stay reviewable.
migrate:
	$(GO) run ./cmd/api -migrate

docker:
	docker compose build

clean:
	rm -rf bin

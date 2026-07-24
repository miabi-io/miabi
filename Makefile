BINARY      := miabi
AGENT       := miabi-agent
PKG         := github.com/miabi-io/miabi
VERSION     ?= dev
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS     := -X $(PKG)/internal/config.Version=$(VERSION) -X $(PKG)/internal/config.CommitID=$(COMMIT)
AGENT_IMAGE ?= ghcr.io/miabi-io/agent

LICENSE_PUBLIC_KEY ?= $(shell cat ../license-public.key 2>/dev/null)
MIABI_LDFLAGS := $(LDFLAGS) -X $(PKG)/internal/enterprise.embeddedPublicKey=$(LICENSE_PUBLIC_KEY)

WEB_DIR := web
EMBED_WEB_DIR := internal/web/dist

.PHONY: run worker agent build build-agent build-ui build-all dev-ui test lint tidy migrate license-tool docker docker-rootless docker-agent compose-up compose-down

run: ## Run the API server
	go run -tags enterprise -ldflags "$(MIABI_LDFLAGS)" ./cmd/miabi server

worker: ## Run the background worker
	go run -tags enterprise -ldflags "$(MIABI_LDFLAGS)" ./cmd/miabi worker

agent: ## Run the node agent (needs MIABI_CONTROL_URL + MIABI_NODE_TOKEN)
	go run -ldflags "$(LDFLAGS)" ./cmd/agent

build: ## Build the control-plane binary
	go build -tags enterprise -ldflags "$(MIABI_LDFLAGS)" -o bin/$(BINARY) ./cmd/miabi

license-tool: ## Build the internal license issuer (holds the signing key; never shipped)
	go build -ldflags "$(LDFLAGS)" -o bin/miabi-license ./cmd/miabi-license

build-agent: ## Build the node agent binary
	go build -ldflags "$(LDFLAGS)" -o bin/$(AGENT) ./cmd/agent

build-ui: ## Build the web UI (Vue) and stage it for embedding (internal/web/dist)
	npm --prefix $(WEB_DIR) ci
	npm --prefix $(WEB_DIR) run build
	# Stage the build output where `go build` embeds it (internal/web), keeping the
	# committed .gitkeep so `go build` still works on a clean tree.
	rm -rf $(EMBED_WEB_DIR)
	cp -r $(WEB_DIR)/dist $(EMBED_WEB_DIR)
	touch $(EMBED_WEB_DIR)/.gitkeep

build-all: build-ui build build-agent ## Build the UI, server binary, and node agent

dev-ui: ## Run the Vite dev server (proxies /api/v1 -> :9000)
	npm --prefix $(WEB_DIR) run dev

test: ## Run tests (enterprise build tag — matches the shipped binary)
	go test -tags enterprise ./...

lint: ## Static analysis
	go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

tidy: ## Sync go.mod / go.sum
	go mod tidy


docker: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg GO_TAGS=enterprise --build-arg LICENSE_PUBLIC_KEY=$(LICENSE_PUBLIC_KEY) \
		-f docker/Dockerfile -t miabi:$(VERSION) .

docker-rootless: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg GO_TAGS=enterprise --build-arg LICENSE_PUBLIC_KEY=$(LICENSE_PUBLIC_KEY) \
		-f docker/Dockerfile.rootless -t miabi:$(VERSION)-rootless .

docker-agent: ## Build the node agent Docker image
	docker build -f Dockerfile.agent -t $(AGENT_IMAGE):$(VERSION) -t $(AGENT_IMAGE):latest .

compose-up: ## Start local dev stack (builds app + Postgres + Redis)
	docker compose -f compose.dev.yaml up -d

compose-down: ## Stop local dev stack
	docker compose -f compose.dev.yaml down

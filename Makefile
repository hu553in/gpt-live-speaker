.DEFAULT_GOAL := check

BUILD_DIR ?= ./dist

PRETTIER := bunx prettier -u
ACTIONLINT := bunx github-actionlint
TAPLO := bunx @taplo/cli
PREK ?= prek

.PHONY: ensure-build-dir
ensure-build-dir:
	mkdir -p -- "$(BUILD_DIR)"

.PHONY: check-workflows
check-workflows:
	$(ACTIONLINT)

.PHONY: check-renovate
check-renovate:
	bunx --package renovate renovate-config-validator --strict --no-global renovate.json

.PHONY: check-hooks
check-hooks:
	$(PREK) validate-config prek.toml

.PHONY: check
check: lint check-hooks build check-deps check-vulns test check-web check-renovate check-workflows

.PHONY: check-fix
check-fix: lint-fix
	$(MAKE) check

.PHONY: install-deps
install-deps:
	go mod download
	cd web && bun ci

# main.go embeds web/dist, so Go builds and linters need the frontend build first.
.PHONY: build-web
build-web: install-deps
	cd web && bun run build

.PHONY: lint
lint: build-web
	$(PRETTIER) -c . '!web/**'
	$(TAPLO) fmt --check
	golangci-lint fmt --diff
	golangci-lint run
	cd web && bun lint

.PHONY: lint-fix
lint-fix: build-web
	$(PRETTIER) -w . '!web/**'
	$(TAPLO) fmt
	golangci-lint fmt
	golangci-lint run --fix
	cd web && bun lint:fix

.PHONY: check-web
check-web: install-deps
	cd web && bun check:types && bun check:unused && bun check:vulns

.PHONY: check-deps
check-deps: install-deps
	go mod tidy -diff
	go mod verify

.PHONY: check-vulns
check-vulns: build-web
	go tool govulncheck ./...

.PHONY: test
test: build-web
	go test -race ./...

.PHONY: build
build: ensure-build-dir build-web
	CGO_ENABLED=0 GOFLAGS="-buildvcs=false" \
	go build -trimpath -ldflags="-s -w" -o "$(BUILD_DIR)/gpt-live-speaker" .

.PHONY: run
run: build-web
	set -a && . ./.env && go run .

.PHONY: clean
clean:
	rm -rf -- "$(BUILD_DIR)" web/dist

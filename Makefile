.DEFAULT_GOAL := check

BUILD_DIR ?= ./dist
WAKEWORD := internal/wakeword

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
check: lint check-hooks check-types build check-deps check-vulns test check-renovate check-workflows

.PHONY: check-fix
check-fix: lint-fix
	$(MAKE) check

.PHONY: install-deps
install-deps:
	go mod download
	uv sync --directory "$(WAKEWORD)" --all-groups --locked

.PHONY: lint
lint: install-deps
	$(PRETTIER) -c .
	$(TAPLO) fmt --check
	golangci-lint fmt --diff
	golangci-lint run
	uv run --directory "$(WAKEWORD)" ruff check
	uv run --directory "$(WAKEWORD)" ruff format --check

.PHONY: lint-fix
lint-fix: install-deps
	$(PRETTIER) -w .
	$(TAPLO) fmt
	golangci-lint fmt
	golangci-lint run --fix
	uv run --directory "$(WAKEWORD)" ruff check --fix
	uv run --directory "$(WAKEWORD)" ruff format

.PHONY: check-types
check-types: install-deps
	uv run --directory "$(WAKEWORD)" ty check

.PHONY: check-deps
check-deps: install-deps
	go mod tidy -diff
	go mod verify
	uv lock --directory "$(WAKEWORD)" --check
	uv run --directory "$(WAKEWORD)" deptry .

.PHONY: check-vulns
check-vulns: install-deps
	go tool govulncheck ./...
	uv run --directory "$(WAKEWORD)" pysentry-rs .

.PHONY: test
test: install-deps
	go test -race ./...

# malgo binds the system audio APIs through cgo.
.PHONY: build
build: ensure-build-dir install-deps
	CGO_ENABLED=1 GOFLAGS="-buildvcs=false" \
	go build -trimpath -ldflags="-s -w" -o "$(BUILD_DIR)/gpt-live-speaker" ./cmd/gpt-live-speaker

.PHONY: run
run:
	set -a && . ./.env && go run ./cmd/gpt-live-speaker

.PHONY: clean
clean:
	rm -rf -- "$(BUILD_DIR)"

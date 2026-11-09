# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: geth evm statesim statesim-measure all test lint fmt clean devtools help

GOBIN = ./build/bin
GO ?= latest
GORUN = go run

#? geth: Build geth.
geth:
	$(GORUN) build/ci.go install ./cmd/geth
	@echo "Done building."
	@echo "Run \"$(GOBIN)/geth\" to launch geth."

#? evm: Build evm.
evm:
	$(GORUN) build/ci.go install ./cmd/evm
	@echo "Done building."
	@echo "Run \"$(GOBIN)/evm\" to launch evm."

# Optional native authentication backend: STATESIM_TAGS=gls254.
STATESIM_TAGS ?=
# Go versions that do not recognize worktree .git files omit automatic VCS data.
STATESIM_REVISION = $(shell git rev-parse --verify HEAD 2>/dev/null || echo unknown)$(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo -dirty)
STATESIM_LDFLAGS = -X main.sourceRevision=$(STATESIM_REVISION)
DB_ENGINE ?= leveldb
DB_SOURCE ?=

#? statesim: Build replay with upstream databases pinned in go.mod.
statesim:
	GOWORK=off go build -ldflags '$(STATESIM_LDFLAGS)' -tags '$(STATESIM_TAGS)' -o $(GOBIN)/statesim ./cmd/statesim

#? statesim-measure: Build with DB_ENGINE=leveldb|pebble and DB_SOURCE=module@branch-or-commit (or local checkout).
statesim-measure:
	@set -eu; \
	case '$(DB_ENGINE)' in \
	  leveldb) module=github.com/syndtr/goleveldb ;; \
	  pebble) module=github.com/cockroachdb/pebble/v2 ;; \
	  *) echo 'DB_ENGINE must be leveldb or pebble' >&2; exit 1 ;; \
	esac; \
	test -n '$(DB_SOURCE)' || { echo 'Set DB_SOURCE to an instrumentation module@branch-or-commit or local checkout' >&2; exit 1; }; \
	mkdir -p build/cache; \
	tmp=$$(mktemp -d "$$PWD/build/cache/statesim-mod.XXXXXX"); \
	trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; \
	cp go.mod "$$tmp/go.mod"; cp go.sum "$$tmp/go.sum"; \
	GOWORK=off go mod edit -modfile="$$tmp/go.mod" -replace="$$module=$(DB_SOURCE)"; \
	GOWORK=off go build -mod=mod -modfile="$$tmp/go.mod" -ldflags '$(STATESIM_LDFLAGS)' -tags '$(STATESIM_TAGS)' \
	  -o $(GOBIN)/statesim-$(DB_ENGINE)-measure ./cmd/statesim

#? all: Build all packages and executables.
all:
	$(GORUN) build/ci.go install

#? test: Run the tests.
test: all
	$(GORUN) build/ci.go test

#? lint: Run certain pre-selected linters.
lint: ## Run linters.
	$(GORUN) build/ci.go lint

#? fmt: Ensure consistent code formatting.
fmt:
	gofmt -s -w $(shell find . -name "*.go")

#? clean: Clean go cache, built executables, and the auto generated folder.
clean:
	go clean -cache
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

#? devtools: Install recommended developer tools.
devtools:
	env GOBIN= go install golang.org/x/tools/cmd/stringer@latest
	env GOBIN= go install github.com/fjl/gencodec@latest
	env GOBIN= go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	env GOBIN= go install ./cmd/abigen
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

#? help: Get more info on make commands.
help: Makefile
	@echo ''
	@echo 'Usage:'
	@echo '  make [target]'
	@echo ''
	@echo 'Targets:'
	@sed -n 's/^#?//p' $< | column -t -s ':' |  sort | sed -e 's/^/ /'

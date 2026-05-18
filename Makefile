CURRENT_DIR=$(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))
TEST?=$$(go list ./... |grep -v 'vendor')
GOFMT_FILES?=$$(find . -name '*.go' |grep -v vendor)
PKG_NAME=kubernetes
export GO111MODULE=on

# Args passed to `go test` for the unit-test target. Race detector + cover.
# `?=` so a value in the environment (CI) overrides the default; otherwise
# `export NAME = VALUE` would silently win over env per GNU make semantics.
TESTARGS ?= -race -coverprofile=coverage.txt -covermode=atomic

# Args passed to `go test` for the acceptance-test target. The SDK v2 testing
# harness keeps one shared `*schema.Provider` per test binary; when multiple
# tests call `t.Parallel()` the harness races on internal GRPCProviderServer
# state during Configure, which `-race` flags even though the tests pass
# functionally. We deliberately omit `-race` here. Override via env if a
# specific run needs different args (e.g. `-run`, lower parallelism).
ACC_TESTARGS ?= -parallel 4

default: build

build:
	go install

dist:
	goreleaser build --single-target --skip-validate --rm-dist

test:
	go test -i $(TEST) || exit 1
	echo $(TEST) | \
		xargs -t -n4 go test $(TESTARGS) -timeout=30s -parallel=4

testacc:
	TF_ACC=1 go test ./kubernetes -v $(ACC_TESTARGS) -timeout 120m -count=1

publish:
	goreleaser release --rm-dist

vet:
	@echo "go vet ."
	@go vet $$(go list ./... | grep -v vendor/) ; if [ $$? -eq 1 ]; then \
		echo ""; \
		echo "Vet found suspicious constructs. Please check the reported constructs"; \
		echo "and fix them if necessary before submitting the code for review."; \
		exit 1; \
	fi

fmt:
	gofmt -w $(GOFMT_FILES)

fmtcheck:
	@sh -c "'$(CURDIR)/scripts/gofmtcheck.sh'"

errcheck:
	@sh -c "'$(CURDIR)/scripts/errcheck.sh'"

.PHONY: build dist test testacc publish vet fmt fmtcheck errcheck

SHELL := /bin/bash
.DEFAULT_GOAL := build
.NOTPARALLEL:

BUILD_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
include $(BUILD_ROOT)/versions.env

export BUILD_ROOT BUILD_VERSION UPSTREAM_COMMIT DOCS_URL UPGRADE_PUBLIC_KEYS RELEASE_EXTRA_TAGS
export CGO_ENABLED := 0
export PATH := $(BUILD_ROOT)/.tools/go$(GO_VERSION)/bin:$(BUILD_ROOT)/.tools/node$(NODE_VERSION)/bin:$(BUILD_ROOT)/.tools/pnpm/node_modules/.bin:$(PATH)
export GOPATH := $(BUILD_ROOT)/.cache/gopath
export GOCACHE := $(BUILD_ROOT)/.cache/go-build
export GOTOOLCHAIN := local
export GOMAXPROCS ?= 1
export GOFLAGS ?= -p=1
export GOMEMLIMIT ?= 384MiB
export NODE_OPTIONS ?= --max-old-space-size=512
export TMPDIR := $(BUILD_ROOT)/.cache/tmp
export npm_config_cache := $(BUILD_ROOT)/.cache/npm
export npm_config_store_dir := $(BUILD_ROOT)/.cache/pnpm
export CI := true
export HUSKY := 0

export INSTALL_DIR ?= /home/administrator/prusa-connect
export SERVICE ?= prusa-connect.service
export HEALTH_URL ?= http://127.0.0.1:8080/api/v1/health

.PHONY: help bootstrap frontend build test smoke check-install install status

help:
	@printf '%s\n' 'make             Build PC with embedded frontend and PCMA adapter' 'make test        Run adapter and SDK tests plus go vet' 'make smoke       Build and test a separate loopback PC instance' 'make install     Build, configure PCMA, install profiles/UI and restart with rollback' 'make status      Read the installed service and version'

bootstrap:
	@bash "$(BUILD_ROOT)/scripts/bootstrap.sh"

frontend: bootstrap
	@bash "$(BUILD_ROOT)/scripts/frontend.sh"

build: frontend
	@bash "$(BUILD_ROOT)/scripts/build.sh"

test: bootstrap
	@cd "$(BUILD_ROOT)/adapter" && go test ./... && go vet ./...

smoke: build
	@python3 "$(BUILD_ROOT)/tests/pc_smoke.py" --require-ui

check-install: build
	@mkdir -p "$(TMPDIR)"
	@python3 "$(BUILD_ROOT)/tests/install_smoke.py"

install: check-install test smoke
	@bash "$(BUILD_ROOT)/scripts/install.sh"

status:
	@systemctl show "$(SERVICE)" -p ActiveState -p SubState -p MainPID -p ExecStart
	@curl --fail --silent --show-error --max-time 5 "$(HEALTH_URL)"
	@printf '\n'

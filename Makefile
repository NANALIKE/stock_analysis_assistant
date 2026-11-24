PYTHON ?= python3
RUFF ?= ruff
CONFIG ?= configs/config.example.yaml

.PHONY: build run init doctor backup test check fmt worker-check

build:
	go build -o bin/stockd ./cmd/stockd
	go build -o bin/stockctl ./cmd/stockctl

run:
	go run ./cmd/stockd -config $(CONFIG)

init:
	go run ./cmd/stockctl init -config $(CONFIG)

doctor:
	go run ./cmd/stockctl doctor -config $(CONFIG)

backup:
	go run ./cmd/stockctl backup -config $(CONFIG)

test:
	go test -timeout 60s ./...
	cd worker && PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m unittest discover -s tests -v

check: test
	go vet ./...
	$(RUFF) check --no-cache worker
	$(RUFF) format --check --no-cache worker
	$(PYTHON) -m openapi_spec_validator api/openapi.yaml
	git diff --check

fmt:
	gofmt -w cmd internal migrations
	$(RUFF) format --no-cache worker

worker-check:
	cd worker && PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m stock_worker < ../testdata/worker-request.json

SHELL := /bin/sh
VERSION ?= dev
GOCMD ?= go
NPM ?= npm

.PHONY: all frontend build test vet lint frontend-test comment-audit openapi-audit verify image clean

all: verify build

frontend:
	cd frontend && $(NPM) ci && $(NPM) run build

build: frontend
	mkdir -p bin
	CGO_ENABLED=0 $(GOCMD) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/acme4go ./cmd/acme4go

test:
	$(GOCMD) test ./...

vet:
	$(GOCMD) vet ./...

lint:
	cd frontend && $(NPM) run lint

frontend-test:
	cd frontend && $(NPM) test

comment-audit:
	$(GOCMD) run ./scripts/comment-audit.go .
	cd frontend && $(NPM) run comment-audit

openapi-audit:
	$(GOCMD) run -tags openapiaudit ./scripts/openapi-audit.go

verify: frontend
	$(MAKE) comment-audit openapi-audit test vet lint frontend-test

image:
	docker build --build-arg VERSION=$(VERSION) -t acme4go:$(VERSION) .

clean:
	$(GOCMD) clean
	rm -rf bin frontend/node_modules

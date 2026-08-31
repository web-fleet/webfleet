NIFT ?= /mnt/data/nift-src/nift/nift
.PHONY: frontend build test verify
frontend:
	$(NIFT) build
	rm -rf internal/server/web/*
	cp -a public/. internal/server/web/
build: frontend
	go build ./cmd/webfleet
test: frontend
	go test ./...
verify: test
	git diff --check

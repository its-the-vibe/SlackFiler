BINARY := slackfiler

.PHONY: build test lint fmt

build:
	go build -o $(BINARY) .

test:
	go test -v -coverprofile=coverage.out ./...

lint:
	go vet ./...

fmt:
	gofmt -w .

.DEFAULT_GOAL := build

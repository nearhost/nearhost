.PHONY: build test race vet

build:
	go build -v -o nearhost ./cmd/nearhost

test:
	go test ./...

race:
	go test -race -timeout 15m ./...

vet:
	go vet ./...

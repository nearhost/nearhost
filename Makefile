.PHONY: build test race vet api-types

build:
	go build -v -o nearhost ./cmd/nearhost

test:
	go test ./...

race:
	go test -race -timeout 15m ./...

vet:
	go vet ./...

api-types:
	cd api/ts && npm ci && npm run generate

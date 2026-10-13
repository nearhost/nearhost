.PHONY: build test vet

build:
	go build -o nearhost ./cmd/nearhost

test:
	go test ./...

vet:
	go vet ./...

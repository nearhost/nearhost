.PHONY: build test vet

build:
	go build -v -o nearhost ./cmd/nearhost

test:
	go test ./...

vet:
	go vet ./...

.PHONY: build test vet

build:
	go build -x -o nearhost ./cmd/nearhost

test:
	go test ./...

vet:
	go vet ./...

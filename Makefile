.PHONY: build test vet lint peer relay genkey

BIN := bin/sabaody

build:
	go build -o $(BIN) .

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

peer:
	go run . --mode=peer

relay:
	go run . --mode=relay

genkey:
	go run . --genkey

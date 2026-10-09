BIN := bin/shiplino

.PHONY: build test lint clean

build:
	go build -o $(BIN) ./cmd/shiplino

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w ." && exit 1)
	go vet ./...

clean:
	rm -rf bin dist

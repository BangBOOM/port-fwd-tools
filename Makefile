.PHONY: build test install clean
build:
	go build -trimpath -o bin/pf ./cmd/pf
test:
	go test -race ./...
install:
	go install ./cmd/pf
clean:
	rm -rf bin dist

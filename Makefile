.PHONY: build test vet check install

BINARY := bin/claude-reviewer

build:
	go build -o $(BINARY) ./cmd/claude-reviewer

test:
	go test ./...

vet:
	go vet ./...

check: test vet build

install: build
	mkdir -p "$(HOME)/.local/bin"
	cp $(BINARY) "$(HOME)/.local/bin/claude-reviewer"
	chmod +x "$(HOME)/.local/bin/claude-reviewer"

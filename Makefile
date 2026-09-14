.PHONY: build test vet clean

build:
	go build -trimpath -o bin/codebase-graph-mcp ./cmd/codebase-graph-mcp
	go build -trimpath -o bin/codebase-graph-setup ./cmd/codebase-graph-setup

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f bin/codebase-graph-mcp bin/codebase-graph-setup

vscode-test:
	cd vscode-extension && npm ci && npm test

vscode-package:
	cd vscode-extension && npm ci && npm run package:local

vscode-install: vscode-package
	code --install-extension vscode-extension/dist/codebase-graph-vscode-darwin-arm64.vsix --force

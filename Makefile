.PHONY: build test vet clean

build:
	go build -trimpath -tags 'grammar_subset grammar_subset_javascript grammar_subset_typescript grammar_subset_tsx grammar_subset_python grammar_subset_rust grammar_subset_java grammar_subset_kotlin grammar_subset_c_sharp' -o bin/codebase-graph-mcp ./cmd/codebase-graph-mcp
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
	code --install-extension vscode-extension/dist/codebase-graph-vscode-universal.vsix --force

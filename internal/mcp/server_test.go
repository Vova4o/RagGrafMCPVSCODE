package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/service"
)

func TestServerInitializeListAndIndex(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeMCPFile(t, filepath.Join(repo, "go.mod"), "module example.com/mcp\n")
	writeMCPFile(t, filepath.Join(repo, "main.go"), "package mcp\nfunc Run() {}\n")
	graphService, err := service.New(repo)
	if err != nil {
		t.Fatalf("service.New() error = %v", err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"prepare_code_context","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"index_repository","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := NewServer(graphService, strings.NewReader(input), &output).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("response lines = %d, want 4: %s", len(lines), output.String())
	}
	var initialized map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialized); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	result := initialized["result"].(map[string]any)
	if result["protocolVersion"] != protocol {
		t.Fatalf("protocolVersion = %v, want %s", result["protocolVersion"], protocol)
	}
	instructions, _ := result["instructions"].(string)
	if !strings.Contains(instructions, "call prepare_code_context before broad repository exploration") ||
		!strings.Contains(instructions, "absolute path of the opened workspace") ||
		!strings.Contains(instructions, "Never infer the workspace from the MCP process working directory") ||
		!strings.Contains(instructions, "call check_index_coverage") {
		t.Fatalf("initialize instructions do not advertise graph-first navigation: %q", instructions)
	}

	var listed map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	listedResult := listed["result"].(map[string]any)
	if len(listedResult["tools"].([]any)) != len(tools()) {
		t.Fatalf("tools/list returned %d tools, want %d", len(listedResult["tools"].([]any)), len(tools()))
	}
	foundPrepare := false
	for _, rawTool := range listedResult["tools"].([]any) {
		listedTool := rawTool.(map[string]any)
		if listedTool["name"] == "prepare_code_context" {
			description := listedTool["description"].(string)
			foundPrepare = strings.Contains(description, "Required first call") &&
				strings.Contains(description, "absolute opened workspace")
		}
	}
	if !foundPrepare {
		t.Fatal("tools/list does not advertise prepare_code_context as the required first call")
	}

	var preparedCall map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &preparedCall); err != nil {
		t.Fatalf("decode prepare_code_context response: %v", err)
	}
	preparedResult := preparedCall["result"].(map[string]any)
	if preparedResult["isError"] == true {
		t.Fatalf("prepare_code_context returned an error: %s", lines[2])
	}
	content := preparedResult["content"].([]any)[0].(map[string]any)
	var prepared map[string]any
	if err := json.Unmarshal([]byte(content["text"].(string)), &prepared); err != nil {
		t.Fatalf("decode prepare response: %v", err)
	}
	if prepared["server_version"] != serverVersion || prepared["graph_schema_version"] != float64(2) {
		t.Fatalf("prepare diagnostics = %#v", prepared)
	}

	var indexedCall map[string]any
	if err := json.Unmarshal([]byte(lines[3]), &indexedCall); err != nil {
		t.Fatalf("decode index_repository response: %v", err)
	}
	indexedResult := indexedCall["result"].(map[string]any)
	if indexedResult["isError"] == true {
		t.Fatalf("index_repository returned an error: %s", lines[3])
	}
	if _, err := os.Stat(filepath.Join(repo, ".codebase-graph", "graph.json")); err != nil {
		t.Fatalf("repository graph was not written: %v", err)
	}
}

func TestServerReturnsToolErrorsAsMCPResults(t *testing.T) {
	t.Parallel()
	graphService, err := service.New("")
	if err != nil {
		t.Fatalf("service.New() error = %v", err)
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_graph","arguments":{"query":"Run"}}}` + "\n"
	var output bytes.Buffer
	if err := NewServer(graphService, strings.NewReader(input), &output).Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output.String(), `"isError":true`) || !strings.Contains(output.String(), "repo_path is required") {
		t.Fatalf("tool error response = %s", output.String())
	}
}

func writeMCPFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

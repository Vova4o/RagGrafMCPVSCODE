package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerMergesAndRemovesOwnedConfiguration(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	binary := filepath.Join(t.TempDir(), "codebase-graph-mcp")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(skill, []byte("---\nname: codebase-graph\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	existing := `{"mcpServers":{"other":{"command":"other"}},"custom":true}`
	if err := os.WriteFile(filepath.Join(repo, ".mcp.json"), []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	installer, err := New(binary, skill)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	installer.verify = successfulVerification
	result, err := installer.Install(context.Background(), repo)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(result.Files) != 6 {
		t.Fatalf("Install().Files = %d, want 6", len(result.Files))
	}
	if result.ServerVersion != "0.2.5" || !result.RestartRequired {
		t.Fatalf("Install() verification = %#v", result)
	}
	assertServers(t, filepath.Join(repo, ".mcp.json"), result.Workspace, true)
	assertServers(t, filepath.Join(repo, ".agents", "mcp_config.json"), result.Workspace, false)
	for _, path := range []string{
		filepath.Join(repo, ".claude", "skills", serverName, "SKILL.md"),
		filepath.Join(repo, ".agents", "skills", serverName, "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("installed skill %s: %v", path, err)
		}
	}
	rulePaths := []string{
		filepath.Join(repo, ".claude", "rules", serverName+".md"),
		filepath.Join(repo, ".agents", "rules", serverName+".md"),
	}
	for _, path := range rulePaths {
		rule, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed agent rule %s: %v", path, err)
		}
		if !strings.Contains(string(rule), "call the codebase-graph MCP tool prepare_code_context before broad repository exploration") {
			t.Fatalf("installed agent rule %s does not require graph preparation: %q", path, rule)
		}
		if !strings.Contains(string(rule), "absolute path of the opened workspace as workspace_path") {
			t.Fatalf("installed agent rule %s does not guard against launcher cwd changes: %q", path, rule)
		}
	}
	antigravityRule, err := os.ReadFile(rulePaths[1])
	if err != nil {
		t.Fatalf("read installed Antigravity rule: %v", err)
	}
	if !strings.Contains(string(antigravityRule), "trigger: always_on") {
		t.Fatalf("installed Antigravity rule is not always on: %q", antigravityRule)
	}

	if _, err := installer.Uninstall(context.Background(), repo); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	assertServerAbsent(t, filepath.Join(repo, ".mcp.json"), "other")
	assertServerAbsent(t, filepath.Join(repo, ".agents", "mcp_config.json"), "")
	for _, path := range rulePaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("agent rule %s still exists after uninstall: %v", path, err)
		}
	}
}

func TestUninstallDoesNotCreateMissingConfiguration(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	binary := filepath.Join(t.TempDir(), "codebase-graph-mcp")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(skill, []byte("---\nname: codebase-graph\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	installer, err := New(binary, skill)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	installer.verify = successfulVerification
	if _, err := installer.Uninstall(context.Background(), repo); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	for _, path := range []string{
		filepath.Join(repo, ".mcp.json"),
		filepath.Join(repo, ".agents", "mcp_config.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("configuration %s was created during uninstall", path)
		}
	}
}

func TestParseVerificationRequiresGraphFirstTools(t *testing.T) {
	t.Parallel()
	valid := []byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"codebase-graph","version":"0.2.5"}}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"prepare_code_context"},{"name":"index_workspace"},{"name":"search_workspace_graph"},{"name":"trace_path"}]}}
`)
	result, err := parseVerification(valid)
	if err != nil {
		t.Fatalf("parseVerification() error = %v", err)
	}
	if result.ServerVersion != "0.2.5" || len(result.Tools) != len(requiredServerTools) {
		t.Fatalf("parseVerification() = %#v", result)
	}

	missing := []byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"codebase-graph","version":"0.1.0"}}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"search_graph"}]}}
`)
	if _, err := parseVerification(missing); err == nil {
		t.Fatal("parseVerification() accepted stale tool metadata")
	}
}

func successfulVerification(context.Context, string) (VerifyResult, error) {
	return VerifyResult{ServerVersion: "0.2.5", Tools: append([]string(nil), requiredServerTools...)}, nil
}

func TestCodexPluginUsesPackagedServerBinary(t *testing.T) {
	t.Parallel()
	config := readTestConfig(t, filepath.Join("..", "..", ".mcp.json"))
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal(".mcp.json has no mcpServers object")
	}
	server, ok := servers[serverName].(map[string]any)
	if !ok {
		t.Fatalf(".mcp.json has no %s server", serverName)
	}
	if command := server["command"]; command != "./bin/codebase-graph-mcp" {
		t.Fatalf("command = %v, want packaged binary", command)
	}
	if cwd := server["cwd"]; cwd != "." {
		t.Fatalf("cwd = %v, want plugin root", cwd)
	}
}

func assertServers(t *testing.T, path, repo string, wantOther bool) {
	t.Helper()
	config := readTestConfig(t, path)
	servers := config["mcpServers"].(map[string]any)
	server, exists := servers[serverName].(map[string]any)
	if !exists {
		t.Fatalf("%s has no %s server", path, serverName)
	}
	if server["command"] == "" {
		t.Fatalf("%s has an empty server command", path)
	}
	if server["cwd"] != repo {
		t.Fatalf("%s server cwd = %v, want %s", path, server["cwd"], repo)
	}
	args, ok := server["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "--workspace" || args[1] != repo {
		t.Fatalf("%s server args = %#v, want [--workspace %s]", path, server["args"], repo)
	}
	_, hasOther := servers["other"]
	if hasOther != wantOther {
		t.Fatalf("%s other server presence = %v, want %v", path, hasOther, wantOther)
	}
	if wantOther && config["custom"] != true {
		t.Fatalf("%s did not preserve custom field", path)
	}
}

func assertServerAbsent(t *testing.T, path, preserved string) {
	t.Helper()
	config := readTestConfig(t, path)
	servers := config["mcpServers"].(map[string]any)
	if _, exists := servers[serverName]; exists {
		t.Fatalf("%s still contains %s", path, serverName)
	}
	if preserved != "" {
		if _, exists := servers[preserved]; !exists {
			t.Fatalf("%s did not preserve %s", path, preserved)
		}
	}
}

func readTestConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	var config map[string]any
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", path, err)
	}
	return config
}

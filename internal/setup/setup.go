// Package setup installs workspace-scoped client configuration.
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const serverName = "codebase-graph"

const graphFirstRule = `# Codebase Graph

- For coding, debugging, refactoring, code review, architecture, navigation, symbol discovery, dependency tracing, callers, callees, or impact analysis, call the codebase-graph MCP tool prepare_code_context before broad repository exploration.
- Pass the absolute path of the opened workspace as workspace_path on the first call. Never infer it from the MCP process working directory because launchers may start servers in plugin or scratch directories.
- When a graph is available, use workspace and repository graph tools as the primary source for structural discovery. Read the returned source before editing.
- Use text search only for exact literals, generated or unsupported files, coverage gaps, or verification after graph navigation.
- Before making a negative or exhaustive source claim, call check_index_coverage and disclose any unindexed files.
- A source graph proves static relationships only. Do not present it as proof of runtime traffic or behavior, and report a missing runtime component as a verification boundary.
`

const antigravityRule = `---
trigger: always_on
---

` + graphFirstRule

var requiredServerTools = []string{
	"prepare_code_context",
	"index_workspace",
	"search_workspace_graph",
	"trace_path",
}

// Installer manages Claude Code and Antigravity project integrations.
type Installer struct {
	binaryPath string
	skillPath  string
	verify     func(context.Context, string) (VerifyResult, error)
}

// New returns an Installer using the supplied server binary and canonical skill.
func New(binaryPath, skillPath string) (*Installer, error) {
	binary, err := canonicalFile(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve server binary: %w", err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		return nil, fmt.Errorf("stat server binary %s: %w", binary, err)
	}
	if info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("server binary %s is not executable", binary)
	}
	skill, err := canonicalFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("resolve skill file: %w", err)
	}
	return &Installer{binaryPath: binary, skillPath: skill, verify: verifyServer}, nil
}

// InstallResult lists the files configured for one repository.
type InstallResult struct {
	Workspace       string   `json:"workspace"`
	Files           []string `json:"files"`
	ServerVersion   string   `json:"server_version,omitempty"`
	VerifiedTools   []string `json:"verified_tools,omitempty"`
	RestartRequired bool     `json:"restart_required,omitempty"`
}

// VerifyResult describes a successful MCP handshake with the configured binary.
type VerifyResult struct {
	ServerVersion string   `json:"server_version"`
	Tools         []string `json:"tools"`
}

// Verify starts the packaged server and checks the capabilities required by clients.
func (i *Installer) Verify(ctx context.Context) (VerifyResult, error) {
	return i.verify(ctx, i.binaryPath)
}

// Install configures both clients to share repository and workspace graphs.
func (i *Installer) Install(ctx context.Context, repoPath string) (InstallResult, error) {
	repo, err := canonicalDirectory(repoPath)
	if err != nil {
		return InstallResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return InstallResult{}, fmt.Errorf("install repository integration: %w", err)
	}
	verified, err := i.Verify(ctx)
	if err != nil {
		return InstallResult{}, fmt.Errorf("verify mcp server before installation: %w", err)
	}

	entry := map[string]any{
		"command": i.binaryPath,
		"args":    []string{"--workspace", repo},
		"cwd":     repo,
	}
	configs := []string{
		filepath.Join(repo, ".mcp.json"),
		filepath.Join(repo, ".agents", "mcp_config.json"),
	}
	for _, path := range configs {
		if err := mergeServer(path, entry); err != nil {
			return InstallResult{}, err
		}
	}

	skill, err := os.ReadFile(i.skillPath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("read canonical skill %s: %w", i.skillPath, err)
	}
	skillPaths := []string{
		filepath.Join(repo, ".claude", "skills", serverName, "SKILL.md"),
		filepath.Join(repo, ".agents", "skills", serverName, "SKILL.md"),
	}
	for _, path := range skillPaths {
		if err := writeAtomic(path, skill, 0o644); err != nil {
			return InstallResult{}, fmt.Errorf("install skill %s: %w", path, err)
		}
	}
	rulePaths := []struct {
		path    string
		content string
	}{
		{path: filepath.Join(repo, ".claude", "rules", serverName+".md"), content: graphFirstRule},
		{path: filepath.Join(repo, ".agents", "rules", serverName+".md"), content: antigravityRule},
	}
	for _, rule := range rulePaths {
		if err := writeAtomic(rule.path, []byte(rule.content), 0o644); err != nil {
			return InstallResult{}, fmt.Errorf("install agent rule %s: %w", rule.path, err)
		}
	}

	return InstallResult{
		Workspace:       repo,
		Files:           append(append(configs, skillPaths...), rulePaths[0].path, rulePaths[1].path),
		ServerVersion:   verified.ServerVersion,
		VerifiedTools:   verified.Tools,
		RestartRequired: true,
	}, nil
}

// Uninstall removes only entries and skills owned by codebase-graph.
func (i *Installer) Uninstall(ctx context.Context, repoPath string) (InstallResult, error) {
	repo, err := canonicalDirectory(repoPath)
	if err != nil {
		return InstallResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return InstallResult{}, fmt.Errorf("uninstall repository integration: %w", err)
	}
	configs := []string{
		filepath.Join(repo, ".mcp.json"),
		filepath.Join(repo, ".agents", "mcp_config.json"),
	}
	for _, path := range configs {
		if err := removeServer(path); err != nil {
			return InstallResult{}, err
		}
	}
	skillPaths := []string{
		filepath.Join(repo, ".claude", "skills", serverName, "SKILL.md"),
		filepath.Join(repo, ".agents", "skills", serverName, "SKILL.md"),
	}
	for _, path := range skillPaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return InstallResult{}, fmt.Errorf("remove skill %s: %w", path, err)
		}
	}
	rulePaths := []string{
		filepath.Join(repo, ".claude", "rules", serverName+".md"),
		filepath.Join(repo, ".agents", "rules", serverName+".md"),
	}
	for _, path := range rulePaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return InstallResult{}, fmt.Errorf("remove agent rule %s: %w", path, err)
		}
	}
	return InstallResult{Workspace: repo, Files: append(append(configs, skillPaths...), rulePaths...)}, nil
}

func mergeServer(path string, entry map[string]any) error {
	config, mode, err := readConfig(path)
	if err != nil {
		return err
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		if config["mcpServers"] != nil {
			return fmt.Errorf("mcp configuration %s has a non-object mcpServers field", path)
		}
		servers = make(map[string]any)
		config["mcpServers"] = servers
	}
	servers[serverName] = entry
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mcp configuration %s: %w", path, err)
	}
	return writeAtomic(path, append(payload, '\n'), mode)
}

func removeServer(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat mcp configuration %s: %w", path, err)
	}
	config, mode, err := readConfig(path)
	if err != nil {
		return err
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return nil
	}
	delete(servers, serverName)
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mcp configuration %s: %w", path, err)
	}
	return writeAtomic(path, append(payload, '\n'), mode)
}

func readConfig(path string) (map[string]any, os.FileMode, error) {
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]any), 0o644, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read mcp configuration %s: %w", path, err)
	}
	var config map[string]any
	if err := json.Unmarshal(payload, &config); err != nil {
		return nil, 0, fmt.Errorf("decode mcp configuration %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat mcp configuration %s: %w", path, err)
	}
	return config, info.Mode().Perm(), nil
}

func writeAtomic(path string, payload []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".codebase-graph-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("set permissions on %s: %w", temporaryPath, err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary file %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary file %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file %s: %w", temporaryPath, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace file %s: %w", path, err)
	}
	return nil
}

func canonicalFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("file path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve file path %s: %w", path, err)
	}
	return filepath.EvalSymlinks(abs)
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("repository path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %s: %w", path, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository symlinks %s: %w", abs, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat repository %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %s is not a directory", abs)
	}
	return filepath.Clean(abs), nil
}

func verifyServer(ctx context.Context, binaryPath string) (VerifyResult, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codebase-graph-setup","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	command := exec.CommandContext(verifyCtx, binaryPath)
	command.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if verifyCtx.Err() != nil {
			return VerifyResult{}, fmt.Errorf("start mcp verification: %w", verifyCtx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return VerifyResult{}, fmt.Errorf("run mcp verification: %s", detail)
	}
	result, err := parseVerification(output)
	if err != nil {
		return VerifyResult{}, err
	}
	return result, nil
}

func parseVerification(output []byte) (VerifyResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var result VerifyResult
	toolSet := make(map[string]struct{})
	for {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := decoder.Decode(&message); err != nil {
			if err == io.EOF {
				break
			}
			return VerifyResult{}, fmt.Errorf("decode mcp verification response: %w", err)
		}
		if message.Error != nil {
			return VerifyResult{}, fmt.Errorf("mcp verification returned error: %s", message.Error.Message)
		}
		switch string(message.ID) {
		case "1":
			var initialized struct {
				ServerInfo struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"serverInfo"`
			}
			if err := json.Unmarshal(message.Result, &initialized); err != nil {
				return VerifyResult{}, fmt.Errorf("decode mcp initialize result: %w", err)
			}
			if initialized.ServerInfo.Name != serverName || initialized.ServerInfo.Version == "" {
				return VerifyResult{}, fmt.Errorf("mcp initialize returned server %q version %q", initialized.ServerInfo.Name, initialized.ServerInfo.Version)
			}
			result.ServerVersion = initialized.ServerInfo.Version
		case "2":
			var listed struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(message.Result, &listed); err != nil {
				return VerifyResult{}, fmt.Errorf("decode mcp tools result: %w", err)
			}
			for _, advertised := range listed.Tools {
				toolSet[advertised.Name] = struct{}{}
			}
		}
	}
	if result.ServerVersion == "" {
		return VerifyResult{}, fmt.Errorf("mcp verification did not receive initialize response")
	}
	for _, required := range requiredServerTools {
		if _, ok := toolSet[required]; !ok {
			return VerifyResult{}, fmt.Errorf("mcp server does not advertise required tool %s", required)
		}
		result.Tools = append(result.Tools, required)
	}
	sort.Strings(result.Tools)
	return result, nil
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vladimirgavrilenko/codebase-graph/internal/setup"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	workspace := flag.String("workspace", "", "workspace to configure")
	repo := flag.String("repo", "", "deprecated alias for --workspace")
	action := flag.String("action", "install", "install, repair, uninstall or verify")
	binary := flag.String("binary", "", "path to codebase-graph-mcp")
	skill := flag.String("skill", "", "path to the canonical SKILL.md")
	flag.Parse()
	if *workspace != "" && *repo != "" {
		return fmt.Errorf("--repo and --workspace cannot be used together")
	}
	configuredPath := *workspace
	if configuredPath == "" {
		configuredPath = *repo
	}
	if configuredPath == "" {
		configuredPath = "."
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve setup executable: %w", err)
	}
	pluginRoot := filepath.Dir(filepath.Dir(executable))
	if *binary == "" {
		*binary = filepath.Join(pluginRoot, "bin", "codebase-graph-mcp")
	}
	if *skill == "" {
		*skill = filepath.Join(pluginRoot, "skills", "codebase-graph", "SKILL.md")
	}

	installer, err := setup.New(*binary, *skill)
	if err != nil {
		return err
	}
	var result any
	switch *action {
	case "install":
		result, err = installer.Install(context.Background(), configuredPath)
	case "repair":
		result, err = setup.RepairStaleConfigs(context.Background(), configuredPath, *binary)
	case "uninstall":
		result, err = installer.Uninstall(context.Background(), configuredPath)
	case "verify":
		result, err = installer.Verify(context.Background())
	default:
		return fmt.Errorf("action must be install, repair, uninstall or verify")
	}
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode setup result: %w", err)
	}
	fmt.Println(string(payload))
	return nil
}

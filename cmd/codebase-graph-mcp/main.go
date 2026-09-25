package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vladimirgavrilenko/codebase-graph/internal/mcp"
	"github.com/vladimirgavrilenko/codebase-graph/internal/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	repo := flag.String("repo", "", "restrict the server to one repository")
	workspace := flag.String("workspace", "", "restrict the server to repositories inside one workspace")
	version := flag.Bool("version", false, "print the server version")
	flag.Parse()
	if *version {
		fmt.Println("codebase-graph-mcp 0.2.6")
		return nil
	}
	if *repo != "" && *workspace != "" {
		return fmt.Errorf("--repo and --workspace cannot be used together")
	}

	fixedRepo := *repo
	if fixedRepo == "" {
		fixedRepo = os.Getenv("CODEBASE_GRAPH_REPO")
	}
	fixedWorkspace := *workspace
	if fixedWorkspace == "" {
		fixedWorkspace = os.Getenv("CODEBASE_GRAPH_WORKSPACE")
	}
	if fixedRepo == "" && fixedWorkspace == "" {
		fixedWorkspace = os.Getenv("CLAUDE_PROJECT_DIR")
	}

	var graphService *service.Service
	var err error
	if fixedWorkspace != "" {
		graphService, err = service.NewWorkspace(fixedWorkspace)
	} else {
		graphService, err = service.New(fixedRepo)
	}
	if err != nil {
		return fmt.Errorf("create graph service: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcp.NewServer(graphService, os.Stdin, os.Stdout).Run(ctx); err != nil {
		return fmt.Errorf("run MCP server: %w", err)
	}
	return nil
}

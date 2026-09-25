package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

const serviceHostsFile = "codebase-graph.services.json"

type serviceHostsConfig struct {
	Services []serviceHostConfig `json:"services"`
}

type serviceHostConfig struct {
	Repository string   `json:"repository"`
	Hosts      []string `json:"hosts"`
}

func loadServiceHosts(workspace string, targets map[string]*graph.Graph) (map[string]string, error) {
	result := make(map[string]string)
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	workspace = filepath.Clean(workspace)

	configPath := filepath.Join(workspace, serviceHostsFile)
	file, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("open service host configuration: %w", err)
	}
	defer file.Close()

	var config serviceHostsConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode service host configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode service host configuration: unexpected data after json value")
		}
		return nil, fmt.Errorf("decode service host configuration: %w", err)
	}

	for _, service := range config.Services {
		repository := strings.TrimSpace(service.Repository)
		if repository == "" || filepath.IsAbs(repository) || strings.Contains(repository, "\\") {
			return nil, fmt.Errorf("invalid repository path %q in service host configuration", service.Repository)
		}
		rel := filepath.Clean(filepath.FromSlash(repository))
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("repository path %q escapes workspace", service.Repository)
		}
		candidate := filepath.Join(workspace, rel)
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve repository path %q: %w", service.Repository, err)
		}
		resolved = filepath.Clean(resolved)
		if err := ensureInside(workspace, resolved); err != nil {
			return nil, fmt.Errorf("repository path %q escapes workspace", service.Repository)
		}
		if _, ok := targets[resolved]; !ok {
			return nil, fmt.Errorf("repository path %q does not match a target graph", service.Repository)
		}

		for _, rawHost := range service.Hosts {
			host, err := normalizeServiceHost(rawHost)
			if err != nil {
				return nil, fmt.Errorf("invalid host %q for repository %q: %w", rawHost, service.Repository, err)
			}
			if previous, exists := result[host]; exists && previous != resolved {
				return nil, fmt.Errorf("host %q is assigned to multiple repositories", host)
			}
			result[host] = resolved
		}
	}
	return result, nil
}

func normalizeServiceHost(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("host must not be empty or contain surrounding whitespace")
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '?' || r == '#' || r == '@' {
			return "", fmt.Errorf("host contains an invalid character")
		}
	}

	host := value
	port := ""
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return "", fmt.Errorf("invalid bracketed ip address")
		}
		address := host[1:end]
		if net.ParseIP(address) == nil || !strings.Contains(address, ":") {
			return "", fmt.Errorf("invalid bracketed ip address")
		}
		host = address
		rest := value[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", fmt.Errorf("invalid host suffix")
			}
			port = rest[1:]
		}
	} else if strings.Count(host, ":") == 1 {
		index := strings.LastIndexByte(host, ':')
		port = host[index+1:]
		host = host[:index]
	} else if strings.Contains(host, ":") {
		return "", fmt.Errorf("ipv6 addresses with ports must be bracketed")
	}
	if host == "" {
		return "", fmt.Errorf("host name must not be empty")
	}
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", fmt.Errorf("port must be a number between 1 and 65535")
		}
	} else if strings.HasSuffix(value, ":") {
		return "", fmt.Errorf("port must not be empty")
	}

	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip == nil {
		host = strings.TrimSuffix(host, ".")
		if host == "" || len(host) > 253 {
			return "", fmt.Errorf("invalid dns host name")
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", fmt.Errorf("invalid dns host name")
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					return "", fmt.Errorf("invalid dns host name")
				}
			}
		}
	}
	if port != "" {
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return host + ":" + port, nil
	}
	return host, nil
}

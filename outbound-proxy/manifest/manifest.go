package manifest

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"outbound-proxy/config"
)

// ToolEntry is a single tool in the manifest.
type ToolEntry struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	BaseURL     string      `json:"base_url,omitempty"`
	Host        string      `json:"host,omitempty"`
	Port        int         `json:"port,omitempty"`
	Database    string      `json:"database,omitempty"`
	Scope       string      `json:"scope"`
	Description string      `json:"description,omitempty"`
	Policy      *PolicyInfo `json:"policy,omitempty"`
	Connection  *ConnInfo   `json:"connection,omitempty"`
}

// PolicyInfo describes the access policy for an HTTP tool.
type PolicyInfo struct {
	Methods   []string `json:"methods,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	RateLimit string   `json:"rate_limit,omitempty"`
}

// ConnInfo describes how to connect to a database tool.
type ConnInfo struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Note     string `json:"note"`
}

// Generate builds the tool manifest JSON from the proxy config.
func Generate(cfg *config.Config) ([]byte, error) {
	var entries []ToolEntry

	for _, t := range cfg.ManagedTools {
		switch strings.ToLower(t.Protocol) {
		case "postgres":
			entries = append(entries, postgresEntry(t))
		default:
			entries = append(entries, httpEntry(t))
		}
	}

	if entries == nil {
		entries = []ToolEntry{}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return data, nil
}

func httpEntry(t config.ManagedTool) ToolEntry {
	return ToolEntry{
		Name:        t.Name,
		Type:        "http",
		BaseURL:     baseURL(t.Match),
		Scope:       httpScope(t.Policy.Methods),
		Description: t.Description,
		Policy:      policyInfo(t.Policy),
	}
}

func postgresEntry(t config.ManagedTool) ToolEntry {
	host, _ := parseHostPort(t.Match, 5432)
	localPort := t.LocalPort
	if localPort == 0 {
		localPort = 5432
	}

	return ToolEntry{
		Name:        t.Name,
		Type:        "postgres",
		Host:        "outbound-proxy",
		Port:        localPort,
		Database:    t.Database,
		Scope:       "read-only",
		Description: t.Description,
		Connection: &ConnInfo{
			User:     host + "-agent",
			Password: "**INJECTED**",
			Note:     "Connect normally. Credentials are injected by proxy.",
		},
	}
}

// baseURL constructs an HTTPS URL from the match pattern.
// "*.github.com" → "https://github.com", "api.openai.com" → "https://api.openai.com"
func baseURL(match string) string {
	host := strings.TrimPrefix(match, "*.")
	host, _, _ = net.SplitHostPort(host) // strip port if present
	if host == "" {
		host = strings.TrimPrefix(match, "*.")
	}
	return "https://" + host
}

// httpScope derives scope from allowed methods.
// If only safe methods (GET, HEAD, OPTIONS) → "read-only", otherwise "read-write".
func httpScope(methods []string) string {
	for _, m := range methods {
		upper := strings.ToUpper(m)
		if upper != "GET" && upper != "HEAD" && upper != "OPTIONS" {
			return "read-write"
		}
	}
	return "read-only"
}

func policyInfo(p config.Policy) *PolicyInfo {
	if len(p.Methods) == 0 && len(p.Paths) == 0 && p.RateLimit == "" {
		return nil
	}
	return &PolicyInfo{
		Methods:   p.Methods,
		Paths:     p.Paths,
		RateLimit: p.RateLimit,
	}
}

func parseHostPort(match string, defaultPort int) (string, int) {
	host, portStr, err := net.SplitHostPort(match)
	if err != nil {
		return match, defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, defaultPort
	}
	return host, port
}

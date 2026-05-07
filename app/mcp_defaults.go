package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sleepysoong/kkode/llm"
)

const (
	defaultContext7URL   = "https://mcp.context7.com/mcp"
	defaultSerenaPackage = "git+https://github.com/oraios/serena"
)

// DefaultMCPDiagnostic은 기본 MCP가 왜 붙었거나 빠졌는지 diagnostics에 노출하기 위한 요약이에요.
type DefaultMCPDiagnostic struct {
	Name    string
	Kind    string
	Status  string
	Message string
}

// DefaultProviderOptions는 kkode가 기본으로 붙일 provider 확장 자산을 만들어요.
// 지금은 Serena(code intelligence)와 Context7(live docs) MCP를 기본 설계값으로 삼아요.
// KKODE_DEFAULT_MCP=0/false/off이면 빈 옵션을 돌려줘요.
func DefaultProviderOptions(root string) ProviderOptions {
	if !EnvBoolDefault("KKODE_DEFAULT_MCP", true) {
		return ProviderOptions{}
	}
	return ProviderOptions{MCPServers: DefaultMCPServers(root)}
}

// DefaultMCPServers는 실행 환경에서 바로 붙일 수 있는 기본 MCP server manifest를 만들어요.
// Serena는 uvx 또는 KKODE_SERENA_COMMAND가 있을 때만 포함해서 없는 바이너리 때문에 기본 실행이 깨지지 않게 해요.
// Context7은 원격 HTTP MCP를 기본으로 사용하고, CONTEXT7_API_KEY가 있으면 header로 전달해요.
func DefaultMCPServers(root string) map[string]llm.MCPServer {
	out := map[string]llm.MCPServer{}
	if server, ok := defaultSerenaServer(root); ok {
		out[server.Name] = server
	}
	if server, ok := defaultContext7Server(); ok {
		out[server.Name] = server
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MergeProviderOptions는 default option 위에 explicit option을 덮어써요.
// 실행 요청이나 저장 resource manifest가 같은 이름을 쓰면 명시 설정이 이겨요.
func MergeProviderOptions(defaults ProviderOptions, explicit ProviderOptions) ProviderOptions {
	out := ProviderOptions{MCPServers: map[string]llm.MCPServer{}}
	for name, server := range defaults.MCPServers {
		out.MCPServers[name] = cloneMCPServer(server)
	}
	for name, server := range explicit.MCPServers {
		out.MCPServers[name] = cloneMCPServer(server)
	}
	if len(out.MCPServers) == 0 {
		out.MCPServers = nil
	}
	out.SkillDirectories = append(append([]string{}, defaults.SkillDirectories...), explicit.SkillDirectories...)
	out.CustomAgents = append(append([]llm.Agent{}, defaults.CustomAgents...), explicit.CustomAgents...)
	return out
}

func defaultSerenaServer(root string) (llm.MCPServer, bool) {
	command := defaultSerenaCommand()
	if command == "" {
		return llm.MCPServer{}, false
	}
	projectRoot := root
	if projectRoot == "" {
		projectRoot = "."
	}
	if abs, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = abs
	}
	args := CSV(os.Getenv("KKODE_SERENA_ARGS"))
	if len(args) == 0 {
		args = []string{"--from", defaultSerenaPackage, "serena", "start-mcp-server", "--context", "ide-assistant", "--project", projectRoot}
	}
	return llm.MCPServer{Kind: llm.MCPStdio, Name: "serena", Tools: []string{"*"}, Timeout: EnvInt("KKODE_SERENA_TIMEOUT", 30), Command: command, Args: args, Cwd: projectRoot}, true
}

func defaultContext7Server() (llm.MCPServer, bool) {
	url := EnvDefault("KKODE_CONTEXT7_URL", defaultContext7URL)
	if url == "" {
		return llm.MCPServer{}, false
	}
	headers := map[string]string{}
	if key := os.Getenv("CONTEXT7_API_KEY"); key != "" {
		headers["CONTEXT7_API_KEY"] = key
	}
	return llm.MCPServer{Kind: llm.MCPHTTP, Name: "context7", Tools: []string{"*"}, Timeout: EnvInt("KKODE_CONTEXT7_TIMEOUT", 30), URL: url, Headers: headers}, true
}

// DefaultMCPDiagnostics는 Serena/Context7 기본 MCP의 운영 상태를 설명해요.
func DefaultMCPDiagnostics(root string) []DefaultMCPDiagnostic {
	if !EnvBoolDefault("KKODE_DEFAULT_MCP", true) {
		return []DefaultMCPDiagnostic{
			{Name: "serena", Kind: string(llm.MCPStdio), Status: "disabled", Message: "KKODE_DEFAULT_MCP가 꺼져 있어요"},
			{Name: "context7", Kind: string(llm.MCPHTTP), Status: "disabled", Message: "KKODE_DEFAULT_MCP가 꺼져 있어요"},
		}
	}
	out := []DefaultMCPDiagnostic{}
	command := defaultSerenaCommand()
	if command == "" {
		out = append(out, DefaultMCPDiagnostic{Name: "serena", Kind: string(llm.MCPStdio), Status: "missing", Message: "uvx 또는 KKODE_SERENA_COMMAND가 없어서 기본 Serena MCP를 자동 연결하지 않아요"})
	} else {
		projectRoot := root
		if projectRoot == "" {
			projectRoot = "."
		}
		if abs, err := filepath.Abs(projectRoot); err == nil {
			projectRoot = abs
		}
		out = append(out, DefaultMCPDiagnostic{Name: "serena", Kind: string(llm.MCPStdio), Status: "configured", Message: "command=" + command + " project=" + projectRoot})
	}
	url := strings.TrimSpace(EnvDefault("KKODE_CONTEXT7_URL", defaultContext7URL))
	if url == "" {
		out = append(out, DefaultMCPDiagnostic{Name: "context7", Kind: string(llm.MCPHTTP), Status: "disabled", Message: "KKODE_CONTEXT7_URL이 비어 있어요"})
	} else {
		message := "url=" + url
		if os.Getenv("CONTEXT7_API_KEY") != "" {
			message += " api_key=configured"
		}
		out = append(out, DefaultMCPDiagnostic{Name: "context7", Kind: string(llm.MCPHTTP), Status: "configured", Message: message})
	}
	return out
}

func defaultSerenaCommand() string {
	command := strings.TrimSpace(EnvDefault("KKODE_SERENA_COMMAND", ""))
	if command != "" {
		return command
	}
	if _, err := exec.LookPath("uvx"); err == nil {
		return "uvx"
	}
	return ""
}

func cloneMCPServer(server llm.MCPServer) llm.MCPServer {
	server.Tools = append([]string{}, server.Tools...)
	server.Args = append([]string{}, server.Args...)
	if server.Env != nil {
		env := make(map[string]string, len(server.Env))
		for k, v := range server.Env {
			env[k] = v
		}
		server.Env = env
	}
	if server.Headers != nil {
		headers := make(map[string]string, len(server.Headers))
		for k, v := range server.Headers {
			headers[k] = v
		}
		server.Headers = headers
	}
	return server
}

package llm

import "strings"

type ApprovalMode string

const (
	ApprovalDenyAll       ApprovalMode = "deny_all"
	ApprovalReadOnly      ApprovalMode = "read_only"
	ApprovalTrustedWrites ApprovalMode = "trusted_writes"
	ApprovalAllowAll      ApprovalMode = "allow_all"
)

type ApprovalPolicy struct {
	Mode            ApprovalMode
	AllowedCommands []string
	AllowedPaths    []string
	AllowedURLs     []string
}

func (p ApprovalPolicy) AllowsRead(path string) bool {
	return p.Mode == ApprovalReadOnly || p.Mode == ApprovalTrustedWrites || p.Mode == ApprovalAllowAll || p.pathAllowed(path)
}

func (p ApprovalPolicy) AllowsWrite(path string) bool {
	switch p.Mode {
	case ApprovalAllowAll:
		return true
	case ApprovalTrustedWrites:
		return p.pathAllowed(path)
	default:
		return false
	}
}

func (p ApprovalPolicy) AllowsCommand(command string) bool {
	if p.Mode == ApprovalAllowAll {
		return true
	}
	for _, allowed := range p.AllowedCommands {
		if command == allowed || strings.HasPrefix(command, allowed+" ") {
			return true
		}
	}
	return false
}

func (p ApprovalPolicy) pathAllowed(path string) bool {
	for _, prefix := range p.AllowedPaths {
		if path == prefix || strings.HasPrefix(path, strings.TrimRight(prefix, "/")+"/") {
			return true
		}
	}
	return false
}

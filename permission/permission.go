package permission

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionAsk   Action = "ask"
	ActionDeny  Action = "deny"
)

type Request struct {
	SessionID string
	Tool      string
	Args      map[string]any
	Command   string
	Path      string
	URL       string
	AgentName string
}

type Decision struct {
	Action Action
	Reason string
	RuleID string
}

type Engine interface {
	Decide(ctx context.Context, req Request) (Decision, error)
}

type Rule struct {
	ID      string
	Tool    string
	Pattern string
	Action  Action
	Reason  string
}

type StaticEngine struct {
	Rules         []Rule
	DefaultAction Action
}

func (e StaticEngine) Decide(ctx context.Context, req Request) (Decision, error) {
	if d, ok := firstMatching(req, e.Rules, ActionDeny); ok {
		return d, nil
	}
	if d, ok := firstMatching(req, e.Rules, ActionAsk); ok {
		return d, nil
	}
	if d, ok := firstMatching(req, e.Rules, ActionAllow); ok {
		return d, nil
	}
	action := e.DefaultAction
	if action == "" {
		action = ActionDeny
	}
	return Decision{Action: action, Reason: "default policy"}, nil
}

func firstMatching(req Request, rules []Rule, action Action) (Decision, bool) {
	for _, rule := range rules {
		if rule.Action != action {
			continue
		}
		if !toolMatches(rule.Tool, req.Tool) {
			continue
		}
		if !patternMatches(rule.Pattern, req) {
			continue
		}
		return Decision{Action: rule.Action, Reason: firstNonEmpty(rule.Reason, "matched permission rule"), RuleID: rule.ID}, true
	}
	return Decision{}, false
}

func toolMatches(pattern, tool string) bool {
	return pattern == "" || pattern == "*" || pattern == tool
}

func patternMatches(pattern string, req Request) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	candidates := []string{req.Path, req.Command, req.URL}
	if strings.HasPrefix(pattern, "domain:") {
		domain := strings.TrimPrefix(pattern, "domain:")
		return strings.Contains(req.URL, "://"+domain) || strings.Contains(req.URL, "."+domain)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if candidate == pattern {
			return true
		}
		if ok, _ := filepath.Match(pattern, candidate); ok {
			return true
		}
		if globMatches(pattern, candidate) {
			return true
		}
		if strings.HasSuffix(pattern, " *") && strings.HasPrefix(candidate, strings.TrimSuffix(pattern, " *")+" ") {
			return true
		}
	}
	return false
}

func globMatches(pattern, value string) bool {
	pattern = filepath.ToSlash(pattern)
	value = filepath.ToSlash(value)
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if ch == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
			continue
		}
		if ch == '?' {
			b.WriteString("[^/]")
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(ch)))
	}
	b.WriteByte('$')
	ok, _ := regexp.MatchString(b.String(), value)
	return ok
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

type GuardrailPolicy struct {
	Name  string
	Check func(text string) error
}

func GuardrailPolicyFunc(name string, check func(text string) error) GuardrailPolicy {
	return GuardrailPolicy{Name: name, Check: check}
}

func JSONRequiredFieldsPolicy(name string, requiredFields ...string) GuardrailPolicy {
	return GuardrailPolicyFunc(name, func(text string) error {
		var obj map[string]any
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			return fmt.Errorf("output must be valid JSON object: %w", err)
		}
		for _, field := range requiredFields {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			if _, ok := obj[field]; !ok {
				return fmt.Errorf("missing required JSON field %q", field)
			}
		}
		return nil
	})
}

func (g Guardrails) CheckInput(input string) error {
	if err := checkBlocked("input", input, g.BlockedSubstrings); err != nil {
		return err
	}
	return checkPolicies("input", input, g.InputPolicies)
}

func (g Guardrails) CheckOutput(output string) error {
	if err := checkBlocked("output", output, g.BlockedOutputSubstrings); err != nil {
		return err
	}
	return checkPolicies("output", output, g.OutputPolicies)
}

func checkBlocked(kind string, text string, blockedSubstrings []string) error {
	lower := strings.ToLower(text)
	for _, blocked := range blockedSubstrings {
		if blocked == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(blocked)) {
			return fmt.Errorf("%s blocked by guardrail: %s", kind, blocked)
		}
	}
	return nil
}

func checkPolicies(kind string, text string, policies []GuardrailPolicy) error {
	for _, policy := range policies {
		if policy.Check == nil {
			continue
		}
		name := strings.TrimSpace(policy.Name)
		if name == "" {
			name = "policy"
		}
		if err := policy.Check(text); err != nil {
			return fmt.Errorf("%s blocked by guardrail policy %s: %w", kind, name, err)
		}
	}
	return nil
}

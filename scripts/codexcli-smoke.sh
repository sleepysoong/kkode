#!/usr/bin/env bash
set -euo pipefail

model="${1:-gpt-5.3-codex}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
cat > "$tmpdir/kkode-codexcli-smoke.go" <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
)

func main() {
	model := "gpt-5.3-codex"
	if len(os.Args) > 1 {
		model = os.Args[1]
	}
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := codexcli.New(codexcli.Config{WorkingDirectory: wd, Ephemeral: true})
	resp, err := client.Generate(ctx, llm.Request{Model: model, Messages: []llm.Message{llm.UserText("Reply with exactly: OK")}})
	if err != nil {
		if isCodexUnavailable(err) {
			fmt.Printf("SKIP: Codex CLI smoke unavailable: %v\n", err)
			return
		}
		panic(err)
	}
	fmt.Println(resp.Text)
}

func isCodexUnavailable(err error) bool {
	text := strings.ToLower(err.Error())
	for _, needle := range []string{"not logged in", "login", "auth", "credential", "usage limit", "failed to load models cache", "codex: not found", "executable file not found"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
GO

go run "$tmpdir/kkode-codexcli-smoke.go" "$model"

#!/usr/bin/env bash
set -euo pipefail

model="${1:-gpt-5-mini}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
cat > "$tmpdir/kkode-copilot-smoke.go" <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	kc "github.com/sleepysoong/kkode/providers/copilot"
)

func main() {
	model := "gpt-5-mini"
	if len(os.Args) > 1 {
		model = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	client := kc.New(kc.Config{ClientName: "kkode-smoke", WorkingDirectory: wd})
	defer client.Close()
	resp, err := client.Generate(ctx, llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.UserText("Reply with exactly: OK")},
	})
	if err != nil {
		if isAuthUnavailable(err) {
			fmt.Printf("SKIP: Copilot smoke unavailable: %v\n", err)
			return
		}
		panic(err)
	}
	fmt.Println(resp.Text)
}

func isAuthUnavailable(err error) bool {
	text := strings.ToLower(err.Error())
	for _, needle := range []string{"auth", "credential", "login", "unauthorized", "forbidden", "not signed in"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
GO

go run "$tmpdir/kkode-copilot-smoke.go" "$model"

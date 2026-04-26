#!/usr/bin/env bash
set -euo pipefail

model="${1:-gpt-5.3-codex}"
cat > /tmp/kkode-codexcli-smoke.go <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
)

func main() {
	model := "gpt-5.3-codex"
	if len(os.Args) > 1 { model = os.Args[1] }
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := codexcli.New(codexcli.Config{WorkingDirectory:".", Ephemeral:true})
	resp, err := client.Generate(ctx, llm.Request{Model:model, Messages: []llm.Message{llm.UserText("Reply with exactly: OK")}})
	if err != nil { panic(err) }
	fmt.Println(resp.Text)
}
GO

go run /tmp/kkode-codexcli-smoke.go "$model"

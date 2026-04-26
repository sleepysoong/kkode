#!/usr/bin/env bash
set -euo pipefail

model="${1:-gpt-5-mini}"
cat > /tmp/kkode-copilot-smoke.go <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
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
	client := kc.New(kc.Config{ClientName: "kkode-smoke", WorkingDirectory: "."})
	defer client.Close()
	resp, err := client.Generate(ctx, llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.UserText("Reply with exactly: OK")},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text)
}
GO

go run /tmp/kkode-copilot-smoke.go "$model"

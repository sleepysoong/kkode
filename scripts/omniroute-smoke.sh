#!/usr/bin/env bash
set -euo pipefail

base="${OMNIROUTE_BASE_URL:-http://localhost:20128/v1}"
admin="${OMNIROUTE_ADMIN_BASE_URL:-${base%/v1}}"
model="${OMNIROUTE_TEST_MODEL:-auto}"

if ! curl -fsS --max-time 2 "$admin/api/monitoring/health" >/dev/null 2>&1; then
  echo "SKIP: OmniRoute is not reachable at $admin"
  exit 0
fi

cat > /tmp/kkode-omniroute-smoke.go <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/omniroute"
)

func main() {
	base := os.Getenv("OMNIROUTE_BASE_URL")
	if base == "" { base = "http://localhost:20128/v1" }
	model := os.Getenv("OMNIROUTE_TEST_MODEL")
	if model == "" { model = "auto" }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := omniroute.New(omniroute.Config{BaseURL: base, APIKey: os.Getenv("OMNIROUTE_API_KEY"), SessionID: "kkode-smoke", NoCache: true})
	health, err := client.Health(ctx)
	if err != nil { panic(err) }
	fmt.Println("health:", health.Status)
	models, err := client.ListModels(ctx)
	if err != nil { panic(err) }
	fmt.Println("models:", len(models.Data))
	resp, err := client.Generate(ctx, llm.Request{Model:model, Messages: []llm.Message{llm.UserText("Reply with exactly: OK")}, MaxOutputTokens: 16})
	if err != nil { panic(err) }
	fmt.Println(resp.Text)
}
GO

go run /tmp/kkode-omniroute-smoke.go

package permission

import (
	"context"
	"testing"
)

func TestStaticEngineDenyWins(t *testing.T) {
	engine := StaticEngine{DefaultAction: ActionAsk, Rules: []Rule{
		{ID: "allow-go", Tool: "bash", Pattern: "go test *", Action: ActionAllow},
		{ID: "deny-rm", Tool: "bash", Pattern: "rm *", Action: ActionDeny},
	}}
	dec, err := engine.Decide(context.Background(), Request{Tool: "bash", Command: "rm -rf ."})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Action != ActionDeny || dec.RuleID != "deny-rm" {
		t.Fatalf("decision=%#v", dec)
	}
	dec, err = engine.Decide(context.Background(), Request{Tool: "bash", Command: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Action != ActionAllow {
		t.Fatalf("decision=%#v", dec)
	}
}

func TestStaticEngineGlob(t *testing.T) {
	engine := StaticEngine{Rules: []Rule{{ID: "deny-git", Tool: "edit", Pattern: ".git/**", Action: ActionDeny}}, DefaultAction: ActionAllow}
	dec, err := engine.Decide(context.Background(), Request{Tool: "edit", Path: ".git/config"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Action != ActionDeny {
		t.Fatalf("decision=%#v", dec)
	}
}

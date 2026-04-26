package transcript

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestSaveLoad(t *testing.T) {
	tr := New("t1")
	tr.Add(llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}}, &llm.Response{Text: "ok"}, errors.New("note"))
	path := t.TempDir() + "/transcript.json"
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "t1" || len(loaded.Turns) != 1 || loaded.Turns[0].Response.Text != "ok" {
		t.Fatalf("loaded=%#v", loaded)
	}
}

func TestSaveRedacted(t *testing.T) {
	tr := New("t2")
	tr.Add(llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("token=abc1234567890secretvalue")}}, nil, nil)
	path := t.TempDir() + "/redacted.json"
	if err := tr.SaveRedacted(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || !strings.Contains(string(b), "[REDACTED]") {
		t.Fatalf("not redacted: %s", b)
	}
}

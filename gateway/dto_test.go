package gateway

import (
	"testing"

	"github.com/sleepysoong/kkode/session"
)

func TestSessionDTOClonesMetadata(t *testing.T) {
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	sess.Metadata["source"] = "original"
	dto := toSessionDTO(sess)
	dto.Metadata["source"] = "changed"
	if sess.Metadata["source"] != "original" {
		t.Fatalf("SessionDTO metadata가 원본 session map을 alias하면 안 돼요: %#v", sess.Metadata)
	}
}

package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunGitCommandBoundsStderr(t *testing.T) {
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\npython3 - <<'PY'\nimport sys\nsys.stderr.write('e' * " + strconv.Itoa(maxGitStderrBytes+10) + ")\nPY\nexit 1\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, _, err := runGitCommand(context.Background(), t.TempDir(), []string{"status"}, 1024)
	if err == nil {
		t.Fatal("failing git command should return an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "[git stderr truncated]") {
		t.Fatalf("bounded git stderr should report truncation: %q", msg)
	}
	if len(msg) > maxGitStderrBytes+512 {
		t.Fatalf("git stderr error should stay bounded: len=%d", len(msg))
	}
}

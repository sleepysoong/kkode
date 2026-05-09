package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
)

const MaxProjectInstructionBytes = 32 << 10

type ProjectInstruction struct {
	Path      string
	Name      string
	Text      string
	Bytes     int
	Truncated bool
}

func LoadProjectInstructions(root string, scopes ...string) []ProjectInstruction {
	if !EnvBoolDefault("KKODE_PROJECT_INSTRUCTIONS", true) {
		return nil
	}
	paths := projectInstructionPaths(root, scopes...)
	out := make([]ProjectInstruction, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		item, ok := readProjectInstruction(abs)
		if ok {
			out = append(out, item)
		}
	}
	return out
}

func ProjectInstructionBlocks(root string, scopes ...string) []string {
	instructions := LoadProjectInstructions(root, scopes...)
	if len(instructions) == 0 {
		return nil
	}
	blocks := make([]string, 0, len(instructions))
	for _, item := range instructions {
		parts := []string{"프로젝트 지침 파일이에요: " + item.Name, "경로: " + item.Path, item.Text}
		if item.Truncated {
			parts = append(parts, "[프로젝트 지침이 길어서 일부만 포함했어요]")
		}
		blocks = append(blocks, strings.Join(parts, "\n\n"))
	}
	return blocks
}

func projectInstructionPaths(root string, scopes ...string) []string {
	paths := []string{}
	if EnvBoolDefault("KKODE_GLOBAL_PROJECT_INSTRUCTIONS", false) {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			paths = append(paths,
				filepath.Join(home, ".kkode", "KKODE.md"),
				filepath.Join(home, ".codex", "AGENTS.md"),
				filepath.Join(home, ".claude", "CLAUDE.md"),
			)
		}
	}
	if root == "" {
		root = "."
	}
	if absRoot, err := filepath.Abs(root); err == nil {
		for _, dir := range projectInstructionDirs(absRoot, scopes...) {
			paths = append(paths,
				filepath.Join(dir, "AGENTS.md"),
				filepath.Join(dir, "CLAUDE.md"),
				filepath.Join(dir, "KKODE.md"),
			)
		}
	}
	return paths
}

func projectInstructionDirs(root string, scopes ...string) []string {
	root = filepath.Clean(root)
	dirs := []string{root}
	seen := map[string]bool{root: true}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || scope == "." {
			continue
		}
		target := scope
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		target = filepath.Clean(target)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			continue
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		current := root
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			if !seen[current] {
				seen[current] = true
				dirs = append(dirs, current)
			}
		}
	}
	return dirs
}

func readProjectInstruction(path string) (ProjectInstruction, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ProjectInstruction{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return ProjectInstruction{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(MaxProjectInstructionBytes)+int64(utf8.UTFMax)))
	if err != nil {
		return ProjectInstruction{}, false
	}
	truncated := len(data) > MaxProjectInstructionBytes
	text := strings.TrimSpace(llm.RedactSecrets(projectInstructionUTF8(string(data), MaxProjectInstructionBytes)))
	if text == "" {
		return ProjectInstruction{}, false
	}
	return ProjectInstruction{Path: path, Name: filepath.Base(path), Text: text, Bytes: len(text), Truncated: truncated}, true
}

func projectInstructionUTF8(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

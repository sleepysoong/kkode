package gateway

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestFeatureCatalogEndpointsExistInOpenAPI(t *testing.T) {
	paths := readOpenAPIPaths(t)
	for _, feature := range DefaultFeatureCatalog() {
		for _, endpoint := range feature.Endpoints {
			method, path, ok := strings.Cut(endpoint, " ")
			if !ok {
				t.Fatalf("feature endpoint 형식이 이상해요: %q", endpoint)
			}
			method = strings.ToLower(strings.TrimSpace(method))
			path = strings.TrimSpace(path)
			methods := paths[path]
			if !methods[method] {
				t.Fatalf("feature endpoint가 OpenAPI paths에 없어요: feature=%s endpoint=%s", feature.Name, endpoint)
			}
		}
	}
}

func TestOpenAPIContainsRunStartManifestFields(t *testing.T) {
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, field := range []string{"mcp_servers:", "skills:", "subagents:"} {
		if !strings.Contains(text, field) {
			t.Fatalf("RunStartRequest OpenAPI schema에 %s 필드가 필요해요", field)
		}
	}
}

func readOpenAPIPaths(t *testing.T) map[string]map[string]bool {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch):$`)
	paths := map[string]map[string]bool{}
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			if paths[current] == nil {
				paths[current] = map[string]bool{}
			}
			continue
		}
		if current == "" {
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			paths[current][m[1]] = true
		}
	}
	if len(paths) == 0 {
		t.Fatal("OpenAPI paths를 읽지 못했어요")
	}
	return paths
}

package gateway

import (
	"os"
	"reflect"
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

func TestOpenAPIOperationsExposeStandardErrorResponse(t *testing.T) {
	ops := readOpenAPIOperationErrorResponses(t)
	for op, hasError := range ops {
		if !hasError {
			t.Fatalf("OpenAPI operation %s에 표준 Error response reference가 필요해요", op)
		}
	}
}

func TestOpenAPISchemaPropertiesMatchCoreDTOs(t *testing.T) {
	schemas := readOpenAPISchemaProperties(t)
	cases := []struct {
		schema string
		dto    any
	}{
		{schema: "HealthResponse", dto: HealthResponse{}},
		{schema: "ReadyResponse", dto: ReadyResponse{}},
		{schema: "VersionResponse", dto: VersionResponse{}},
		{schema: "APIIndexResponse", dto: APIIndexResponse{}},
		{schema: "ErrorEnvelope", dto: ErrorEnvelope{}},
		{schema: "Error", dto: ErrorDTO{}},
		{schema: "CapabilityResponse", dto: CapabilityResponse{}},
		{schema: "DiagnosticsResponse", dto: DiagnosticsResponse{}},
		{schema: "DiagnosticCheck", dto: DiagnosticCheckDTO{}},
		{schema: "RunRuntimeStats", dto: RunRuntimeStatsDTO{}},
		{schema: "Limit", dto: LimitDTO{}},
		{schema: "ProviderListResponse", dto: ProviderListResponse{}},
		{schema: "ProviderTestRequest", dto: ProviderTestRequest{}},
		{schema: "ProviderTestResponse", dto: ProviderTestResponse{}},
		{schema: "ProviderTestResult", dto: ProviderTestResultDTO{}},
		{schema: "Provider", dto: ProviderDTO{}},
		{schema: "Conversion", dto: ConversionDTO{}},
		{schema: "Route", dto: RouteDTO{}},
		{schema: "ModelListResponse", dto: ModelListResponse{}},
		{schema: "Model", dto: ModelDTO{}},
		{schema: "SessionListResponse", dto: SessionListResponse{}},
		{schema: "SessionCreateRequest", dto: SessionCreateRequest{}},
		{schema: "SessionExportResponse", dto: SessionExportResponse{}},
		{schema: "SessionExportCounts", dto: SessionExportCountsDTO{}},
		{schema: "SessionImportRequest", dto: SessionImportRequest{}},
		{schema: "SessionImportResponse", dto: SessionImportResponse{}},
		{schema: "TranscriptResponse", dto: TranscriptResponse{}},
		{schema: "RunTranscriptResponse", dto: RunTranscriptResponse{}},
		{schema: "RunStartRequest", dto: RunStartRequest{}},
		{schema: "RunPreviewResponse", dto: RunPreviewResponse{}},
		{schema: "ProviderRequestPreview", dto: ProviderRequestPreviewDTO{}},
		{schema: "Run", dto: RunDTO{}},
		{schema: "RunEvent", dto: RunEventDTO{}},
		{schema: "RequestCorrelationResponse", dto: RequestCorrelationResponse{}},
		{schema: "RequestCorrelationEventsResponse", dto: RequestCorrelationEventsResponse{}},
		{schema: "RequestCorrelationTranscriptResponse", dto: RequestCorrelationTranscriptResponse{}},
		{schema: "ToolListResponse", dto: ToolListResponse{}},
		{schema: "Tool", dto: ToolDTO{}},
		{schema: "ToolCallRequest", dto: ToolCallRequest{}},
		{schema: "ToolCallResponse", dto: ToolCallResponse{}},
		{schema: "FileGlobResponse", dto: FileGlobResponse{}},
		{schema: "FileGrepResponse", dto: FileGrepResponse{}},
		{schema: "FileGrepMatch", dto: FileGrepMatchDTO{}},
		{schema: "FilePatchRequest", dto: FilePatchRequest{}},
		{schema: "FilePatchResponse", dto: FilePatchResponse{}},
		{schema: "FileWriteRequest", dto: FileWriteRequest{}},
		{schema: "LSPSymbolListResponse", dto: LSPSymbolListResponse{}},
		{schema: "LSPLocationListResponse", dto: LSPLocationListResponse{}},
		{schema: "LSPReferenceListResponse", dto: LSPReferenceListResponse{}},
		{schema: "LSPDiagnosticListResponse", dto: LSPDiagnosticListResponse{}},
		{schema: "LSPHoverResponse", dto: LSPHoverResponse{}},
		{schema: "CheckpointListResponse", dto: CheckpointListResponse{}},
		{schema: "ResourceListResponse", dto: ResourceListResponse{}},
		{schema: "Resource", dto: ResourceDTO{}},
		{schema: "GitStatusResponse", dto: GitStatusResponse{}},
		{schema: "GitStatusEntry", dto: GitStatusEntryDTO{}},
		{schema: "GitDiffResponse", dto: GitDiffResponse{}},
		{schema: "GitLogResponse", dto: GitLogResponse{}},
		{schema: "GitLogEntry", dto: GitLogEntryDTO{}},
		{schema: "StatsResponse", dto: StatsResponse{}},
	}
	for _, tc := range cases {
		props := schemas[tc.schema]
		if len(props) == 0 {
			t.Fatalf("OpenAPI schema %s properties를 찾지 못했어요", tc.schema)
		}
		for _, field := range jsonFieldNames(tc.dto) {
			if !props[field] {
				t.Fatalf("OpenAPI schema %s에 DTO json field %s가 빠졌어요", tc.schema, field)
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

func readOpenAPIOperationErrorResponses(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|options):$`)
	out := map[string]bool{}
	currentPath := ""
	currentOp := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			currentPath = m[1]
			currentOp = ""
			continue
		}
		if currentPath == "" {
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			currentOp = m[1] + " " + currentPath
			out[currentOp] = false
			continue
		}
		if currentOp != "" && strings.Contains(line, "#/components/responses/Error") {
			out[currentOp] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI operation을 읽지 못했어요")
	}
	return out
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

func readOpenAPISchemaProperties(t *testing.T) map[string]map[string]bool {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schemaRe := regexp.MustCompile(`^    ([A-Za-z0-9]+):$`)
	propRe := regexp.MustCompile(`^        ([A-Za-z0-9_]+):$`)
	out := map[string]map[string]bool{}
	inSchemas := false
	inProperties := false
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "  schemas:" {
			inSchemas = true
			continue
		}
		if !inSchemas {
			continue
		}
		if m := schemaRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			inProperties = false
			if out[current] == nil {
				out[current] = map[string]bool{}
			}
			continue
		}
		if current == "" {
			continue
		}
		if strings.TrimSpace(line) == "properties:" {
			inProperties = true
			continue
		}
		if !inProperties {
			continue
		}
		if m := propRe.FindStringSubmatch(line); m != nil {
			out[current][m[1]] = true
		}
	}
	return out
}

func jsonFieldNames(dto any) []string {
	t := reflect.TypeOf(dto)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := []string{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		out = append(out, name)
	}
	return out
}

package gateway

import (
	"os"
	"reflect"
	"regexp"
	"sort"
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

func TestAPIIndexLinksExistInOpenAPI(t *testing.T) {
	paths := readOpenAPIPaths(t)
	linkMethods := apiIndexLinkMethods()
	for name, path := range APIIndexLinks() {
		method := "get"
		if explicitMethod := linkMethods[name]; explicitMethod != "" {
			method = strings.ToLower(explicitMethod)
		}
		methods := paths[path]
		if !methods[method] {
			t.Fatalf("API index link가 OpenAPI paths에 없어요: link=%s method=%s path=%s", name, method, path)
		}
	}
}

func TestFeatureCatalogEndpointsExistInAPIIndexLinks(t *testing.T) {
	linkMethods := apiIndexLinkMethods()
	indexEndpoints := map[string]bool{}
	for name, path := range APIIndexLinks() {
		method := "GET"
		if explicitMethod := linkMethods[name]; explicitMethod != "" {
			method = explicitMethod
		}
		indexEndpoints[method+" "+path] = true
	}
	for _, feature := range DefaultFeatureCatalog() {
		for _, endpoint := range feature.Endpoints {
			if !indexEndpoints[endpoint] {
				t.Fatalf("feature endpoint가 API index links에 없어요: feature=%s endpoint=%s", feature.Name, endpoint)
			}
		}
	}
}

func TestAPIIndexOperationsMatchLinks(t *testing.T) {
	links := APIIndexLinks()
	linkMethods := apiIndexLinkMethods()
	operations := APIIndexOperations()
	if len(operations) != len(links) {
		t.Fatalf("API index operations count = %d, links count = %d", len(operations), len(links))
	}
	seen := map[string]bool{}
	for i, op := range operations {
		if i > 0 && operations[i-1].Name > op.Name {
			t.Fatalf("API index operations must be name-sorted: previous=%s current=%s", operations[i-1].Name, op.Name)
		}
		if seen[op.Name] {
			t.Fatalf("API index operation name duplicated: %s", op.Name)
		}
		seen[op.Name] = true
		if links[op.Name] != op.Path {
			t.Fatalf("API index operation %s path = %q, link path = %q", op.Name, op.Path, links[op.Name])
		}
		wantMethod := "GET"
		if explicitMethod := linkMethods[op.Name]; explicitMethod != "" {
			wantMethod = explicitMethod
		}
		if op.Method != wantMethod {
			t.Fatalf("API index operation %s method = %q, want %q", op.Name, op.Method, wantMethod)
		}
		if op.Method != strings.ToUpper(op.Method) {
			t.Fatalf("API index operation %s method must be uppercase: %q", op.Name, op.Method)
		}
		switch op.Method {
		case "GET", "POST", "PUT", "DELETE":
		default:
			t.Fatalf("API index operation %s has unsupported method %q", op.Name, op.Method)
		}
	}
	for name := range links {
		if !seen[name] {
			t.Fatalf("API index link has no operation metadata: %s", name)
		}
	}
}

func TestOpenAPIRequiresAPIIndexOperations(t *testing.T) {
	required := readOpenAPISchemaRequired(t, "APIIndexResponse")
	for _, field := range []string{"version", "links", "operations"} {
		if !required[field] {
			t.Fatalf("APIIndexResponse OpenAPI required에 %s 필드가 필요해요: %+v", field, sortedKeys(required))
		}
	}
}

func TestAPIIndexRequiredFieldsAreNotOmitEmpty(t *testing.T) {
	required := readOpenAPISchemaRequired(t, "APIIndexResponse")
	omitEmpty := jsonOmitEmptyFields(APIIndexResponse{})
	for _, field := range sortedKeys(required) {
		if omitEmpty[field] {
			t.Fatalf("APIIndexResponse required field %s must not use omitempty", field)
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

func TestOpenAPIOperationsExposeUniqueOperationIDs(t *testing.T) {
	operations := readOpenAPIOperationIDs(t)
	seen := map[string]string{}
	for op, id := range operations {
		if strings.TrimSpace(id) == "" {
			t.Fatalf("OpenAPI operation %s에 operationId가 필요해요", op)
		}
		if previous := seen[id]; previous != "" {
			t.Fatalf("OpenAPI operationId가 중복됐어요: id=%s first=%s second=%s", id, previous, op)
		}
		seen[id] = op
	}
}

func TestOpenAPIOperationsExposeSuccessResponse(t *testing.T) {
	statuses := readOpenAPIOperationResponseStatuses(t)
	for op, responses := range statuses {
		hasSuccess := false
		for _, status := range responses {
			if strings.HasPrefix(status, "2") {
				hasSuccess = true
				break
			}
		}
		if !hasSuccess {
			t.Fatalf("OpenAPI operation %s에 2xx 성공 response가 필요해요: %+v", op, responses)
		}
	}
}

func TestOpenAPIDescriptionsQuoteColonScalars(t *testing.T) {
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	unquotedColonScalar := regexp.MustCompile(`^\s+(description|summary):\s+[^'"|>].*:\s+`)
	for i, line := range strings.Split(string(data), "\n") {
		if unquotedColonScalar.MatchString(line) {
			t.Fatalf("OpenAPI line %d has an unquoted colon scalar that breaks YAML parsers: %s", i+1, line)
		}
	}
}

func TestOpenAPISuccessResponsesExposeContent(t *testing.T) {
	responses := readOpenAPIOperationSuccessResponseContent(t)
	for op, statuses := range responses {
		for status, hasContent := range statuses {
			if status == "204" {
				continue
			}
			if !hasContent {
				t.Fatalf("OpenAPI operation %s의 %s 성공 response에 content schema가 필요해요", op, status)
			}
		}
	}
}

func TestOpenAPIPathParametersAreDeclared(t *testing.T) {
	operations := readOpenAPIPathParameterDeclarations(t)
	for op, contract := range operations {
		for _, name := range sortedKeys(contract.expected) {
			if !contract.declared[name] {
				t.Fatalf("OpenAPI operation %s 경로 변수 {%s}가 parameters에 선언되지 않았어요", op, name)
			}
		}
		for _, name := range sortedKeys(contract.declared) {
			if !contract.expected[name] {
				t.Fatalf("OpenAPI operation %s에 경로에 없는 path parameter %s가 선언됐어요", op, name)
			}
		}
	}
}

func TestOpenAPIOperationParametersAreUnique(t *testing.T) {
	parameters := readOpenAPIOperationParameters(t)
	for op, names := range parameters {
		seen := map[string]bool{}
		for _, name := range names {
			if seen[name] {
				t.Fatalf("OpenAPI operation %s has duplicate parameter %s", op, name)
			}
			seen[name] = true
		}
	}
}

func TestOpenAPIComponentReferencesExist(t *testing.T) {
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	components := readOpenAPIComponentNames(t, text)
	refRe := regexp.MustCompile(`\$ref:\s*'?#/components/([^/'"]+)/([^'"]+)'?`)
	for _, match := range refRe.FindAllStringSubmatch(text, -1) {
		section, name := match[1], match[2]
		if !components[section][name] {
			t.Fatalf("OpenAPI component reference가 존재하지 않아요: section=%s name=%s", section, name)
		}
	}
}

type dtoSchemaCase struct {
	schema string
	dto    any
}

func TestOpenAPISchemaPropertiesMatchCoreDTOs(t *testing.T) {
	schemas := readOpenAPISchemaProperties(t)
	for _, tc := range coreDTOSchemaCases() {
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

func TestOpenAPISchemaContractCoversGatewayDTOs(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range coreDTOSchemaCases() {
		typeName := reflect.TypeOf(tc.dto).Name()
		covered[typeName] = true
		if strings.HasSuffix(typeName, "DTO") {
			covered[strings.TrimSuffix(typeName, "DTO")] = true
		}
	}
	for _, typeName := range exportedGatewayDTOTypeNames(t) {
		if !covered[typeName] {
			t.Fatalf("gateway DTO %s가 OpenAPI schema property 계약 테스트에 빠졌어요", typeName)
		}
	}
}

func TestOpenAPIStatsResponseRequiresDashboardTotals(t *testing.T) {
	required := readOpenAPISchemaRequired(t, "StatsResponse")
	for _, field := range []string{"sessions", "sessions_by_provider", "sessions_by_model", "sessions_by_mode", "turns", "events", "events_by_type", "run_events", "run_events_by_type", "todos", "todos_by_status", "checkpoints", "artifacts", "artifacts_by_kind", "total_runs", "runs", "run_duration", "run_duration_by_provider", "run_duration_by_model", "run_usage", "run_usage_by_provider", "run_usage_by_model", "total_resources", "resources"} {
		if !required[field] {
			t.Fatalf("StatsResponse OpenAPI required에 %s 필드가 필요해요: %+v", field, sortedKeys(required))
		}
	}
}

func coreDTOSchemaCases() []dtoSchemaCase {
	return []dtoSchemaCase{
		{schema: "HealthResponse", dto: HealthResponse{}},
		{schema: "ReadyResponse", dto: ReadyResponse{}},
		{schema: "VersionResponse", dto: VersionResponse{}},
		{schema: "APIIndexResponse", dto: APIIndexResponse{}},
		{schema: "APIIndexOperation", dto: APIIndexOperationDTO{}},
		{schema: "ErrorEnvelope", dto: ErrorEnvelope{}},
		{schema: "Error", dto: ErrorDTO{}},
		{schema: "CapabilityResponse", dto: CapabilityResponse{}},
		{schema: "ProviderCapabilityKey", dto: ProviderCapabilityKeyDTO{}},
		{schema: "ProviderPipelineStage", dto: ProviderPipelineStageDTO{}},
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
		{schema: "Session", dto: SessionDTO{}},
		{schema: "SessionCreateRequest", dto: SessionCreateRequest{}},
		{schema: "TurnListResponse", dto: TurnListResponse{}},
		{schema: "Turn", dto: TurnDTO{}},
		{schema: "Message", dto: MessageDTO{}},
		{schema: "Usage", dto: UsageDTO{}},
		{schema: "EventListResponse", dto: EventListResponse{}},
		{schema: "Event", dto: EventDTO{}},
		{schema: "TodoListResponse", dto: TodoListResponse{}},
		{schema: "Todo", dto: TodoDTO{}},
		{schema: "SessionExportResponse", dto: SessionExportResponse{}},
		{schema: "SessionExportCounts", dto: SessionExportCountsDTO{}},
		{schema: "SessionImportRequest", dto: SessionImportRequest{}},
		{schema: "SessionImportResponse", dto: SessionImportResponse{}},
		{schema: "TranscriptResponse", dto: TranscriptResponse{}},
		{schema: "RunTranscriptResponse", dto: RunTranscriptResponse{}},
		{schema: "RunStartRequest", dto: RunStartRequest{}},
		{schema: "RunPreviewResponse", dto: RunPreviewResponse{}},
		{schema: "ProviderRequestPreview", dto: ProviderRequestPreviewDTO{}},
		{schema: "ProviderRoutePreview", dto: ProviderRoutePreviewDTO{}},
		{schema: "RunValidateResponse", dto: RunValidateResponse{}},
		{schema: "RunListResponse", dto: RunListResponse{}},
		{schema: "Run", dto: RunDTO{}},
		{schema: "RunEventListResponse", dto: RunEventListResponse{}},
		{schema: "RunEvent", dto: RunEventDTO{}},
		{schema: "RequestCorrelationResponse", dto: RequestCorrelationResponse{}},
		{schema: "RequestCorrelationEventsResponse", dto: RequestCorrelationEventsResponse{}},
		{schema: "RequestCorrelationTranscriptResponse", dto: RequestCorrelationTranscriptResponse{}},
		{schema: "Feature", dto: FeatureDTO{}},
		{schema: "ToolListResponse", dto: ToolListResponse{}},
		{schema: "Tool", dto: ToolDTO{}},
		{schema: "ToolCallRequest", dto: ToolCallRequest{}},
		{schema: "ToolCallResponse", dto: ToolCallResponse{}},
		{schema: "FileGlobResponse", dto: FileGlobResponse{}},
		{schema: "FileGrepResponse", dto: FileGrepResponse{}},
		{schema: "FileGrepMatch", dto: FileGrepMatchDTO{}},
		{schema: "FilePatchRequest", dto: FilePatchRequest{}},
		{schema: "FilePatchResponse", dto: FilePatchResponse{}},
		{schema: "FileDeleteRequest", dto: FileDeleteRequest{}},
		{schema: "FileDeleteResponse", dto: FileDeleteResponse{}},
		{schema: "FileMoveRequest", dto: FileMoveRequest{}},
		{schema: "FileMoveResponse", dto: FileMoveResponse{}},
		{schema: "FileRestoreRequest", dto: FileRestoreRequest{}},
		{schema: "FileRestoreResponse", dto: FileRestoreResponse{}},
		{schema: "FileCheckpointListResponse", dto: FileCheckpointListResponse{}},
		{schema: "FileCheckpoint", dto: FileCheckpointDTO{}},
		{schema: "FileCheckpointDeleteResponse", dto: FileCheckpointDeleteResponse{}},
		{schema: "FileCheckpointPruneRequest", dto: FileCheckpointPruneRequest{}},
		{schema: "FileCheckpointPruneResponse", dto: FileCheckpointPruneResponse{}},
		{schema: "FileWriteRequest", dto: FileWriteRequest{}},
		{schema: "LSPSymbolListResponse", dto: LSPSymbolListResponse{}},
		{schema: "LSPLocationListResponse", dto: LSPLocationListResponse{}},
		{schema: "LSPReferenceListResponse", dto: LSPReferenceListResponse{}},
		{schema: "LSPRenamePreviewResponse", dto: LSPRenamePreviewResponse{}},
		{schema: "LSPFormatPreviewResponse", dto: LSPFormatPreviewResponse{}},
		{schema: "LSPDiagnosticListResponse", dto: LSPDiagnosticListResponse{}},
		{schema: "LSPHoverResponse", dto: LSPHoverResponse{}},
		{schema: "CheckpointListResponse", dto: CheckpointListResponse{}},
		{schema: "ArtifactListResponse", dto: ArtifactListResponse{}},
		{schema: "ArtifactPruneRequest", dto: ArtifactPruneRequest{}},
		{schema: "ArtifactPruneResponse", dto: ArtifactPruneResponse{}},
		{schema: "Artifact", dto: ArtifactDTO{}},
		{schema: "ResourceListResponse", dto: ResourceListResponse{}},
		{schema: "Resource", dto: ResourceDTO{}},

		{schema: "Checkpoint", dto: CheckpointDTO{}},
		{schema: "SessionCompactRequest", dto: SessionCompactRequest{}},
		{schema: "SessionCompactResponse", dto: SessionCompactResponse{}},
		{schema: "FileListResponse", dto: FileListResponse{}},
		{schema: "FileEntry", dto: FileEntryDTO{}},
		{schema: "FileContentResponse", dto: FileContentResponse{}},
		{schema: "LSPSymbol", dto: LSPSymbolDTO{}},
		{schema: "LSPReference", dto: LSPReferenceDTO{}},
		{schema: "LSPRenameEdit", dto: LSPRenameEditDTO{}},
		{schema: "LSPDiagnostic", dto: LSPDiagnosticDTO{}},
		{schema: "MCPToolListResponse", dto: MCPToolListResponse{}},
		{schema: "MCPTool", dto: MCPToolDTO{}},
		{schema: "MCPResourceListResponse", dto: MCPResourceListResponse{}},
		{schema: "MCPResource", dto: MCPResourceDTO{}},
		{schema: "MCPResourceReadResponse", dto: MCPResourceReadResponse{}},
		{schema: "MCPResourceContent", dto: MCPResourceContentDTO{}},
		{schema: "MCPPromptListResponse", dto: MCPPromptListResponse{}},
		{schema: "MCPPrompt", dto: MCPPromptDTO{}},
		{schema: "MCPPromptArgument", dto: MCPPromptArgumentDTO{}},
		{schema: "MCPPromptGetRequest", dto: MCPPromptGetRequest{}},
		{schema: "MCPPromptGetResponse", dto: MCPPromptGetResponse{}},
		{schema: "MCPPromptMessage", dto: MCPPromptMessageDTO{}},
		{schema: "MCPToolCallRequest", dto: MCPToolCallRequest{}},
		{schema: "MCPToolCallResponse", dto: MCPToolCallResponse{}},
		{schema: "PromptTemplateListResponse", dto: PromptTemplateListResponse{}},
		{schema: "PromptTemplate", dto: PromptTemplateDTO{}},
		{schema: "PromptTemplateResponse", dto: PromptTemplateResponse{}},
		{schema: "PromptRenderRequest", dto: PromptRenderRequest{}},
		{schema: "PromptRenderResponse", dto: PromptRenderResponse{}},
		{schema: "SkillPreviewResponse", dto: SkillPreviewResponse{}},
		{schema: "SubagentPreviewResponse", dto: SubagentPreviewResponse{}},
		{schema: "GitStatusResponse", dto: GitStatusResponse{}},
		{schema: "GitStatusEntry", dto: GitStatusEntryDTO{}},
		{schema: "GitDiffResponse", dto: GitDiffResponse{}},
		{schema: "GitLogResponse", dto: GitLogResponse{}},
		{schema: "GitLogEntry", dto: GitLogEntryDTO{}},
		{schema: "RunDurationStats", dto: RunDurationStatsDTO{}},
		{schema: "StatsResponse", dto: StatsResponse{}},
	}
}

func TestOpenAPIContainsRunStartManifestFields(t *testing.T) {
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, field := range []string{"working_directory:", "max_output_tokens:", "mcp_servers:", "skills:", "subagents:", "enabled_tools:", "disabled_tools:", "context_blocks:"} {
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

func readOpenAPIOperationIDs(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|options):$`)
	operationIDRe := regexp.MustCompile(`^      operationId:\s*([A-Za-z0-9_]+)\s*$`)
	out := map[string]string{}
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
			out[currentOp] = ""
			continue
		}
		if currentOp != "" {
			if m := operationIDRe.FindStringSubmatch(line); m != nil {
				out[currentOp] = m[1]
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI operation을 읽지 못했어요")
	}
	return out
}

func readOpenAPIOperationResponseStatuses(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|options):$`)
	statusRe := regexp.MustCompile(`^        '?([0-9]{3}|default)'?:`)
	out := map[string][]string{}
	currentPath := ""
	currentOp := ""
	inResponses := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "components:" {
			break
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			currentPath = m[1]
			currentOp = ""
			inResponses = false
			continue
		}
		if currentPath == "" {
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			currentOp = m[1] + " " + currentPath
			out[currentOp] = nil
			inResponses = false
			continue
		}
		if currentOp == "" {
			continue
		}
		if strings.TrimSpace(line) == "responses:" {
			inResponses = true
			continue
		}
		if inResponses {
			if m := statusRe.FindStringSubmatch(line); m != nil {
				out[currentOp] = append(out[currentOp], m[1])
				continue
			}
			if strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "        ") && strings.TrimSpace(line) != "" {
				inResponses = false
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI operation response를 읽지 못했어요")
	}
	return out
}

func readOpenAPIOperationSuccessResponseContent(t *testing.T) map[string]map[string]bool {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|options):$`)
	statusRe := regexp.MustCompile(`^        '?([0-9]{3}|default)'?:`)
	out := map[string]map[string]bool{}
	currentPath := ""
	currentOp := ""
	currentStatus := ""
	currentHasContent := false
	inResponses := false
	flush := func() {
		if currentOp == "" || currentStatus == "" || !strings.HasPrefix(currentStatus, "2") {
			return
		}
		if out[currentOp] == nil {
			out[currentOp] = map[string]bool{}
		}
		out[currentOp][currentStatus] = currentHasContent
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "components:" {
			flush()
			break
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			flush()
			currentPath = m[1]
			currentOp = ""
			currentStatus = ""
			currentHasContent = false
			inResponses = false
			continue
		}
		if currentPath == "" {
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			flush()
			currentOp = m[1] + " " + currentPath
			currentStatus = ""
			currentHasContent = false
			inResponses = false
			continue
		}
		if currentOp == "" {
			continue
		}
		if strings.TrimSpace(line) == "responses:" {
			inResponses = true
			continue
		}
		if !inResponses {
			continue
		}
		if m := statusRe.FindStringSubmatch(line); m != nil {
			flush()
			currentStatus = m[1]
			currentHasContent = false
			continue
		}
		if currentStatus != "" && strings.TrimSpace(line) == "content:" {
			currentHasContent = true
			continue
		}
		if strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "        ") && strings.TrimSpace(line) != "" {
			flush()
			currentStatus = ""
			currentHasContent = false
			inResponses = false
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI 2xx response content를 읽지 못했어요")
	}
	return out
}

type openAPIPathParameterContract struct {
	expected map[string]bool
	declared map[string]bool
}

func readOpenAPIPathParameterDeclarations(t *testing.T) map[string]openAPIPathParameterContract {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|options):$`)
	pathParamRe := regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)
	nameRe := regexp.MustCompile(`^\s*name:\s*([A-Za-z0-9_]+)\s*$`)
	refRe := regexp.MustCompile(`\$ref:\s*'?#/components/parameters/([A-Za-z0-9_]+)'?`)
	parameterRefs := readOpenAPIPathParameterComponents(t, string(data))
	out := map[string]openAPIPathParameterContract{}
	currentPath := ""
	currentOp := ""
	pathParamItem := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "components:" {
			break
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			currentPath = m[1]
			currentOp = ""
			pathParamItem = false
			continue
		}
		if currentPath == "" {
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			currentOp = m[1] + " " + currentPath
			contract := openAPIPathParameterContract{expected: map[string]bool{}, declared: map[string]bool{}}
			for _, param := range pathParamRe.FindAllStringSubmatch(currentPath, -1) {
				contract.expected[param[1]] = true
			}
			if len(contract.expected) > 0 {
				out[currentOp] = contract
			}
			pathParamItem = false
			continue
		}
		if currentOp == "" {
			continue
		}
		contract, ok := out[currentOp]
		if !ok {
			continue
		}
		if m := refRe.FindStringSubmatch(line); m != nil {
			if name := parameterRefs[m[1]]; name != "" {
				contract.declared[name] = true
				out[currentOp] = contract
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			pathParamItem = false
		}
		if trimmed == "- in: path" || trimmed == "in: path" {
			pathParamItem = true
			continue
		}
		if pathParamItem {
			if m := nameRe.FindStringSubmatch(line); m != nil {
				contract.declared[m[1]] = true
				out[currentOp] = contract
				pathParamItem = false
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI path parameter operation을 읽지 못했어요")
	}
	return out
}

func readOpenAPIPathParameterComponents(t *testing.T, text string) map[string]string {
	t.Helper()
	out := map[string]string{}
	inParameters := false
	current := ""
	currentIsPath := false
	componentRe := regexp.MustCompile(`^    ([A-Za-z0-9_]+):$`)
	nameRe := regexp.MustCompile(`^\s*name:\s*([A-Za-z0-9_]+)\s*$`)
	for _, line := range strings.Split(text, "\n") {
		if line == "  parameters:" {
			inParameters = true
			current = ""
			currentIsPath = false
			continue
		}
		if !inParameters {
			continue
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
			break
		}
		if m := componentRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			currentIsPath = false
			continue
		}
		if current == "" {
			continue
		}
		if strings.TrimSpace(line) == "in: path" {
			currentIsPath = true
			continue
		}
		if currentIsPath {
			if m := nameRe.FindStringSubmatch(line); m != nil {
				out[current] = m[1]
			}
		}
	}
	return out
}

func readOpenAPIOperationParameters(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/[^:]+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|delete|patch|options):$`)
	refRe := regexp.MustCompile(`\$ref:\s*'?#/components/parameters/([A-Za-z0-9_]+)'?`)
	inRe := regexp.MustCompile(`^\s*-?\s*in:\s*([A-Za-z0-9_]+)\s*$`)
	nameRe := regexp.MustCompile(`^\s*name:\s*([A-Za-z0-9_-]+)\s*$`)
	out := map[string][]string{}
	currentPath := ""
	currentOp := ""
	inParameters := false
	directIn := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "components:" {
			break
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			currentPath = m[1]
			currentOp = ""
			inParameters = false
			directIn = ""
			continue
		}
		if currentPath == "" {
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			currentOp = m[1] + " " + currentPath
			out[currentOp] = nil
			inParameters = false
			directIn = ""
			continue
		}
		if currentOp == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "parameters:" {
			inParameters = true
			directIn = ""
			continue
		}
		if !inParameters {
			continue
		}
		if strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "        ") && trimmed != "" && trimmed != "parameters:" {
			inParameters = false
			directIn = ""
			continue
		}
		if m := refRe.FindStringSubmatch(line); m != nil {
			out[currentOp] = append(out[currentOp], "ref:"+m[1])
			directIn = ""
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			directIn = ""
		}
		if m := inRe.FindStringSubmatch(line); m != nil {
			directIn = m[1]
			continue
		}
		if directIn != "" {
			if m := nameRe.FindStringSubmatch(line); m != nil {
				out[currentOp] = append(out[currentOp], directIn+":"+m[1])
				directIn = ""
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI operation parameter를 읽지 못했어요")
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

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
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

func readOpenAPISchemaRequired(t *testing.T, schema string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schemaRe := regexp.MustCompile(`^    ([A-Za-z0-9]+):$`)
	requiredRe := regexp.MustCompile(`^\s*required:\s*\[([^\]]*)\]`)
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := schemaRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if current != schema {
			continue
		}
		m := requiredRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out := map[string]bool{}
		for _, field := range strings.Split(m[1], ",") {
			field = strings.TrimSpace(field)
			if field != "" {
				out[field] = true
			}
		}
		return out
	}
	t.Fatalf("OpenAPI schema %s required 목록을 찾지 못했어요", schema)
	return nil
}

func readOpenAPIComponentNames(t *testing.T, text string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	sectionRe := regexp.MustCompile(`^  ([A-Za-z0-9_]+):$`)
	nameRe := regexp.MustCompile(`^    ([A-Za-z0-9_]+):$`)
	inComponents := false
	currentSection := ""
	for _, line := range strings.Split(text, "\n") {
		if line == "components:" {
			inComponents = true
			continue
		}
		if !inComponents {
			continue
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
			if m := sectionRe.FindStringSubmatch(line); m != nil {
				currentSection = m[1]
				if out[currentSection] == nil {
					out[currentSection] = map[string]bool{}
				}
				continue
			}
			currentSection = ""
			continue
		}
		if currentSection == "" {
			continue
		}
		if m := nameRe.FindStringSubmatch(line); m != nil {
			out[currentSection][m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("OpenAPI components를 읽지 못했어요")
	}
	return out
}

func exportedGatewayDTOTypeNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	typeRe := regexp.MustCompile(`(?m)^type\s+([A-Z][A-Za-z0-9]*(?:Response|Request|DTO))\s+struct\s+\{`)
	out := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range typeRe.FindAllStringSubmatch(string(data), -1) {
			out = append(out, match[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("gateway 공개 DTO 타입을 찾지 못했어요")
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

func jsonOmitEmptyFields(dto any) map[string]bool {
	t := reflect.TypeOf(dto)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		for _, opt := range strings.Split(opts, ",") {
			if opt == "omitempty" {
				out[name] = true
			}
		}
	}
	return out
}

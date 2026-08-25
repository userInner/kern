package httpapi

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var pathParameterPattern = regexp.MustCompile(`\{[^}/]+\}`)
var schemaReferencePattern = regexp.MustCompile(`#/components/schemas/([A-Za-z][A-Za-z0-9]*)`)

func TestOpenAPIRoutesMatchRuntime(t *testing.T) {
	t.Parallel()

	specRoutes := readOpenAPIRoutes(t)
	runtimeRoutes := make(map[string]struct{})
	for _, route := range (&Server{}).apiRoutes() {
		key := contractRoute(route.method, route.path)
		if _, exists := runtimeRoutes[key]; exists {
			t.Fatalf("duplicate runtime route %s", key)
		}
		runtimeRoutes[key] = struct{}{}
	}

	if missing, extra := routeDifference(runtimeRoutes, specRoutes), routeDifference(specRoutes, runtimeRoutes); len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("OpenAPI route mismatch\nmissing from spec: %v\nmissing from runtime: %v", missing, extra)
	}
}

func TestOpenAPIOperationIDsAndSchemaReferencesAreComplete(t *testing.T) {
	t.Parallel()

	content := readOpenAPIContent(t)
	operationIDs, definitions := openAPIContractSymbols(t, content)
	if len(operationIDs) == 0 || len(definitions) == 0 {
		t.Fatalf("OpenAPI contract is incomplete: operations=%d schemas=%d", len(operationIDs), len(definitions))
	}
	for _, match := range schemaReferencePattern.FindAllSubmatch(content, -1) {
		name := string(match[1])
		if _, exists := definitions[name]; !exists {
			t.Fatalf("OpenAPI references undefined schema %q", name)
		}
	}
}

func TestOpenAPIContractSymbolsAcceptCRLF(t *testing.T) {
	t.Parallel()

	content := []byte("paths:\r\n  /health:\r\n    get:\r\n      operationId: getHealth\r\ncomponents:\r\n  schemas:\r\n    Health:\r\n      type: object\r\n")
	operationIDs, definitions := openAPIContractSymbols(t, content)
	if operationIDs["getHealth"] == 0 {
		t.Fatal("operationId getHealth was not found")
	}
	if _, exists := definitions["Health"]; !exists {
		t.Fatal("schema Health was not found")
	}
}

func openAPIContractSymbols(t *testing.T, content []byte) (map[string]int, map[string]struct{}) {
	t.Helper()

	operationIDs := make(map[string]int)
	definitions := make(map[string]struct{})
	inSchemas := false
	// Git may materialize text files with CRLF on Windows. The OpenAPI
	// contract's indentation is significant to this lightweight parser, but
	// its line-ending representation is not.
	for lineNumber, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "operationId:") {
			operationID := strings.TrimSpace(strings.TrimPrefix(trimmed, "operationId:"))
			if previous := operationIDs[operationID]; operationID == "" || previous != 0 {
				t.Fatalf("invalid or duplicate operationId %q at lines %d and %d", operationID, previous, lineNumber+1)
			}
			operationIDs[operationID] = lineNumber + 1
		}
		switch {
		case line == "  schemas:":
			inSchemas = true
		case inSchemas && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && trimmed != "":
			inSchemas = false
		case inSchemas && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") && strings.HasSuffix(trimmed, ":"):
			name := strings.TrimSuffix(trimmed, ":")
			if _, exists := definitions[name]; exists {
				t.Fatalf("duplicate schema definition %q at line %d", name, lineNumber+1)
			}
			definitions[name] = struct{}{}
		}
	}
	return operationIDs, definitions
}

func TestEveryCookieAuthenticatedMutationHasOriginGate(t *testing.T) {
	t.Parallel()
	server := &Server{token: "session-secret"}
	for _, route := range server.apiRoutes() {
		if route.method == http.MethodGet || route.method == http.MethodHead {
			continue
		}
		route := route
		t.Run(contractRoute(route.method, route.path), func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(route.method, "/", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session-secret"})
			response := httptest.NewRecorder()
			route.handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
		})
	}
}

func readOpenAPIRoutes(t *testing.T) map[string]struct{} {
	t.Helper()
	file, err := os.Open(openAPIPath(t))
	if err != nil {
		t.Fatalf("open OpenAPI contract: %v", err)
	}
	defer file.Close()

	routes := make(map[string]struct{})
	currentPath := ""
	inPaths := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line == "components:":
			inPaths = false
		case inPaths && strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			currentPath = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case inPaths && currentPath != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      "):
			method := strings.TrimSuffix(strings.TrimSpace(line), ":")
			switch method {
			case "get", "post", "put", "patch", "delete":
				key := contractRoute(method, currentPath)
				if _, exists := routes[key]; exists {
					t.Fatalf("duplicate OpenAPI route %s", key)
				}
				routes[key] = struct{}{}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan OpenAPI contract: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("OpenAPI contract contains no routes")
	}
	return routes
}

func readOpenAPIContent(t *testing.T) []byte {
	t.Helper()
	content, err := os.ReadFile(openAPIPath(t))
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	return content
}

func openAPIPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Join(filepath.Dir(filename), "../../../api/openapi.yaml")
}

func contractRoute(method, path string) string {
	return strings.ToUpper(method) + " " + pathParameterPattern.ReplaceAllString(path, "{}")
}

func routeDifference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for route := range left {
		if _, ok := right[route]; !ok {
			difference = append(difference, route)
		}
	}
	sort.Strings(difference)
	return difference
}

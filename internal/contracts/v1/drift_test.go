package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestContractSchemasParse(t *testing.T) {
	for _, schemaPath := range schemaPaths(t) {
		t.Run(filepath.Base(schemaPath), func(t *testing.T) {
			compiler := newContractCompiler(t)
			if _, err := compiler.Compile(schemaURL(schemaPath)); err != nil {
				t.Fatalf("schema should compile: %v", err)
			}
		})
	}
}

func TestValidExamplesPassSchemaGoValidationAndMarshalBackToSchema(t *testing.T) {
	schema := compileDiagnosisSchema(t)

	for _, examplePath := range examplePaths(t, "../../../contracts/v1/examples/valid/*.json") {
		t.Run(filepath.Base(examplePath), func(t *testing.T) {
			var document any
			decodeJSONFile(t, examplePath, &document)
			if err := schema.Validate(document); err != nil {
				t.Fatalf("valid example should pass schema validation: %v", err)
			}

			var result DiagnosisResult
			decodeJSONFile(t, examplePath, &result)
			if err := result.Validate(); err != nil {
				t.Fatalf("valid example should pass Go validation: %v", err)
			}

			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal Go struct: %v", err)
			}

			var roundTrip any
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatalf("unmarshal marshaled Go struct: %v", err)
			}
			if err := schema.Validate(roundTrip); err != nil {
				t.Fatalf("marshaled Go struct should still pass schema validation: %v", err)
			}
		})
	}
}

func TestInvalidExamplesFailSchemaOrGoValidation(t *testing.T) {
	schema := compileDiagnosisSchema(t)

	for _, examplePath := range examplePaths(t, "../../../contracts/v1/examples/invalid/*.json") {
		t.Run(filepath.Base(examplePath), func(t *testing.T) {
			var document any
			decodeJSONFile(t, examplePath, &document)
			schemaErr := schema.Validate(document)

			var result DiagnosisResult
			decodeErr := decodeJSONFileErr(examplePath, &result)
			var goErr error
			if decodeErr == nil {
				goErr = result.Validate()
			}

			if schemaErr == nil && goErr == nil {
				t.Fatal("invalid example should fail schema validation or Go validation")
			}
		})
	}
}

func compileDiagnosisSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	compiler := newContractCompiler(t)
	schema, err := compiler.Compile(schemaURL("../../../contracts/v1/schemas/diagnosis-result.schema.json"))
	if err != nil {
		t.Fatalf("compile diagnosis schema: %v", err)
	}
	return schema
}

func newContractCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()

	compiler := jsonschema.NewCompiler()
	for _, path := range schemaPaths(t) {
		var document map[string]any
		decodeJSONFile(t, path, &document)

		id, ok := document["$id"].(string)
		if !ok || id == "" {
			t.Fatalf("%s must include $id", path)
		}
		if err := compiler.AddResource(id, document); err != nil {
			t.Fatalf("add schema resource %s: %v", id, err)
		}
	}
	return compiler
}

func schemaPaths(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob("../../../contracts/v1/schemas/*.schema.json")
	if err != nil {
		t.Fatalf("glob schemas: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one schema")
	}
	return paths
}

func examplePaths(t *testing.T, pattern string) []string {
	t.Helper()

	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("expected examples for pattern %s", pattern)
	}
	return paths
}

func schemaURL(path string) string {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		panic(err)
	}
	return "file://" + filepath.ToSlash(abs)
}

func decodeJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	if err := decodeJSONFileErr(path, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func decodeJSONFileErr(path string, target any) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

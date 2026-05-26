package v1

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	for _, examplePath := range examplePaths(t, "../../../contracts/v1/examples/valid/*.json") {
		t.Run(filepath.Base(examplePath), func(t *testing.T) {
			schema := compileSchemaForExample(t, examplePath)
			var document any
			decodeJSONFile(t, examplePath, &document)
			if err := schema.Validate(document); err != nil {
				t.Fatalf("valid example should pass schema validation: %v", err)
			}

			contract := decodeContractForExample(t, examplePath)
			if err := contract.Validate(); err != nil {
				t.Fatalf("valid example should pass Go validation: %v", err)
			}

			encoded, err := json.Marshal(contract)
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
	for _, examplePath := range examplePaths(t, "../../../contracts/v1/examples/invalid/*.json") {
		t.Run(filepath.Base(examplePath), func(t *testing.T) {
			schema := compileSchemaForExample(t, examplePath)
			var document any
			decodeJSONFile(t, examplePath, &document)
			schemaErr := schema.Validate(document)

			contract, decodeErr := decodeContractForExampleErr(examplePath)
			var goErr error
			if decodeErr == nil {
				goErr = contract.Validate()
			}

			if schemaErr == nil && goErr == nil {
				t.Fatal("invalid example should fail schema validation or Go validation")
			}
		})
	}
}

func compileSchemaForExample(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()

	schemaURL, err := schemaURLForExample(path)
	if err != nil {
		t.Fatalf("read schema_url from example: %v", err)
	}
	compiler := newContractCompiler(t)
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile schema %s: %v", schemaURL, err)
	}
	return schema
}

func schemaURLForExample(path string) (string, error) {
	var envelope struct {
		SchemaURL string `json:"schema_url"`
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	if envelope.SchemaURL != "" {
		return envelope.SchemaURL, nil
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", err
	}
	return inferSchemaURL(document)
}

func inferSchemaURL(document map[string]any) (string, error) {
	switch {
	case hasKeys(document, "diagnosis_id", "remediation_plan_id", "remediation_attempt_id", "action_taken"):
		return ReceiptSchemaURL, nil
	case hasKeys(document, "remediation_attempt_id", "event_type"):
		return RemediationEventSchemaURL, nil
	case hasKeys(document, "remediation_plan_id", "reason", "requested_by", "status"):
		return ApprovalRequestSchemaURL, nil
	case hasKeys(document, "primary_service", "active_branches", "queued_branches"):
		return InvestigationClusterSchemaURL, nil
	case hasKeys(document, "cluster_id", "branch_type", "source_request_ids"):
		return InvestigationBranchSchemaURL, nil
	default:
		return "", fmt.Errorf("schema_url is required when schema cannot be inferred")
	}
}

func hasKeys(document map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := document[key]; !ok {
			return false
		}
	}
	return true
}

type contract interface {
	Validate() error
}

func decodeContractForExample(t *testing.T, path string) contract {
	t.Helper()
	contract, err := decodeContractForExampleErr(path)
	if err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	return contract
}

func decodeContractForExampleErr(path string) (contract, error) {
	schemaURL, err := schemaURLForExample(path)
	if err != nil {
		return nil, err
	}

	switch schemaURL {
	case DiagnosisSchemaURL:
		var value DiagnosisResult
		return value, decodeJSONFileErr(path, &value)
	case InvestigationRequestSchemaURL:
		var value InvestigationRequest
		return value, decodeJSONFileErr(path, &value)
	case InvestigationClusterSchemaURL:
		var value InvestigationCluster
		return value, decodeJSONFileErr(path, &value)
	case InvestigationBranchSchemaURL:
		var value InvestigationBranch
		return value, decodeJSONFileErr(path, &value)
	case InvestigationDecisionSchemaURL:
		var value InvestigationDecision
		return value, decodeJSONFileErr(path, &value)
	case RemediationPlanSchemaURL:
		var value RemediationPlan
		return value, decodeJSONFileErr(path, &value)
	case ApprovalRequestSchemaURL:
		var value ApprovalRequest
		return value, decodeJSONFileErr(path, &value)
	case RemediationAttemptSchemaURL:
		var value RemediationAttempt
		return value, decodeJSONFileErr(path, &value)
	case RemediationEventSchemaURL:
		var value RemediationEvent
		return value, decodeJSONFileErr(path, &value)
	case ReceiptSchemaURL:
		var value Receipt
		return value, decodeJSONFileErr(path, &value)
	default:
		return nil, fmt.Errorf("unsupported schema_url %s", schemaURL)
	}
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
	slashPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
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

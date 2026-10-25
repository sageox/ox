package attestpublication

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type contractCase struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Valid bool   `json:"valid"`
}

func TestVendoredContractConformance(t *testing.T) {
	indexRaw, err := os.ReadFile("contract/testdata/conformance/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Cases []contractCase `json:"cases"`
	}
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		t.Fatal(err)
	}
	for _, test := range index.Cases {
		t.Run(test.Name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("contract/testdata/conformance", filepath.FromSlash(test.Path)))
			if err != nil {
				t.Fatal(err)
			}
			var schemaErr, semanticErr error
			switch test.Kind {
			case "run":
				schemaErr = validateJSON(runSchema, raw)
				if schemaErr == nil {
					semanticErr = validateRunSemantics(raw)
				}
			case "manifest":
				schemaErr = validateJSON(manifestSchema, raw)
				if schemaErr == nil {
					semanticErr = validateManifestSemantics(raw)
				}
			case "observations":
				schemaErr = validateVendoredSchema(t, "observations.schema.json", "", raw)
				if schemaErr == nil {
					semanticErr = validateObservationsSemantics(raw)
				}
			case "workflow":
				schemaErr = validateVendoredSchema(t, "workflow.schema.json", "", raw)
			case "lifecycle":
				schemaErr = validateVendoredSchema(t, "lifecycle.schema.json", "", raw)
			default:
				const prefix = "control:"
				if len(test.Kind) <= len(prefix) || test.Kind[:len(prefix)] != prefix {
					t.Fatalf("unknown conformance kind %q", test.Kind)
				}
				schemaErr = validateVendoredSchema(t, "control.schema.json", test.Kind[len(prefix):], raw)
			}
			if test.Valid && (schemaErr != nil || semanticErr != nil) {
				t.Fatalf("valid shared fixture rejected: schema=%v semantic=%v", schemaErr, semanticErr)
			}
			if !test.Valid && schemaErr == nil && semanticErr == nil {
				t.Fatal("invalid shared fixture accepted")
			}
		})
	}
}

func validateVendoredSchema(t *testing.T, filename, fragment string, raw []byte) error {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	targetID := ""
	entries, err := os.ReadDir("contract/schema/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		schemaRaw, err := os.ReadFile(filepath.Join("contract/schema/v1", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(schemaRaw, &metadata); err != nil || metadata.ID == "" {
			t.Fatalf("read schema ID for %s: %v", entry.Name(), err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(metadata.ID, document); err != nil {
			t.Fatal(err)
		}
		if entry.Name() == filename {
			targetID = metadata.ID
		}
	}
	if targetID == "" {
		t.Fatalf("vendored schema %s is missing", filename)
	}
	if fragment != "" {
		targetID += "#/$defs/" + fragment
	}
	schema, err := compiler.Compile(targetID)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

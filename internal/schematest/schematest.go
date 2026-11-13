// Package schematest validates JSON against the embedded schemas. It is for
// tests only and must never be imported by non-test code.
package schematest

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Hedronite/scry/schema"
)

func compile(url string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

func validate(url string, raw, data []byte) error {
	s, err := compile(url, raw)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parse instance: %w", err)
	}
	return s.Validate(inst)
}

// Status validates data against scry.status/v0.
func Status(data []byte) error {
	return validate("https://github.com/Hedronite/scry/schema/scry.status.v0.json", schema.StatusV0, data)
}

// Version validates data against scry.version/v0.
func Version(data []byte) error {
	return validate("https://github.com/Hedronite/scry/schema/scry.version.v0.json", schema.VersionV0, data)
}

// StatusEnum returns the allowed values of an enum at the given JSON pointer
// path inside the status schema, for sync tests.
func StatusReasons() ([]string, error) {
	var s struct {
		Properties struct {
			Reasons struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"reasons"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema.StatusV0, &s); err != nil {
		return nil, err
	}
	return s.Properties.Reasons.Items.Enum, nil
}

package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type rejectingWebSchemaLoader struct{}

func (rejectingWebSchemaLoader) Load(rawURL string) (any, error) {
	return nil, fmt.Errorf("external JSON Schema reference is not allowed: %s", rawURL)
}

func decodeExactJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("one JSON value required")
	}
	return value, nil
}

func decodeExactJSONObject(raw []byte) (map[string]any, error) {
	value, err := decodeExactJSONValue(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, errors.New("JSON object required")
	}
	return object, nil
}

func compileWebSchema(schema map[string]any) (*jsonschema.Schema, error) {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectingWebSchemaLoader{})
	const location = "urn:m365-copilot2api:web-schema"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func validateWebSchemaValue(schema map[string]any, value any) error {
	compiled, err := compileWebSchema(schema)
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

func validateToolArgumentsRaw(raw json.RawMessage, fn map[string]any) error {
	arguments, err := decodeExactJSONObject(raw)
	if err != nil {
		return err
	}
	return schemaValid(arguments, fn)
}

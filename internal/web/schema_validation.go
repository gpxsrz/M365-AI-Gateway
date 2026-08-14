package web

import (
	"encoding/json"
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"m365-native/internal/jsonschemautil"
)

func decodeExactJSONValue(raw []byte) (any, error) {
	var value any
	if err := jsonschemautil.DecodeExact(raw, &value); err != nil {
		return nil, err
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
	return jsonschemautil.Compile(schema, "urn:m365-copilot2api:web-schema")
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

package web

func toolFunction(name string, tools []map[string]any) map[string]any {
	for _, t := range tools {
		f, _ := t["function"].(map[string]any)
		if n, _ := f["name"].(string); n == name {
			return f
		}
	}
	return nil
}

func schemaValid(args map[string]any, fn map[string]any) error {
	params, _ := fn["parameters"].(map[string]any)
	if params == nil {
		return nil
	}
	return validateWebSchemaValue(params, args)
}

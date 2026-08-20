package tools

// StringArg reads a required string argument named key out of args.
func StringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", &ArgumentError{Field: key, Reason: "is required"}
	}

	s, ok := v.(string)
	if !ok {
		return "", &ArgumentError{Field: key, Reason: "must be a string"}
	}

	return s, nil
}

// OptionalStringArg reads an optional string argument named key out of
// args, returning def if it's absent.
func OptionalStringArg(args map[string]any, key, def string) string {
	v, ok := args[key]
	if !ok {
		return def
	}

	s, ok := v.(string)
	if !ok {
		return def
	}

	return s
}

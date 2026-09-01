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
// args, returning def if it's absent. If the key is present but not a
// string, it returns an ArgumentError instead of silently falling back to
// the default. This prevents the model from hiding intent behind a wrong
// type (e.g. {"path":12345} silently becoming ".").
func OptionalStringArg(args map[string]any, key, def string) (string, error) {
	v, ok := args[key]
	if !ok {
		return def, nil
	}

	s, ok := v.(string)
	if !ok {
		return "", &ArgumentError{Field: key, Reason: "must be a string"}
	}

	return s, nil
}

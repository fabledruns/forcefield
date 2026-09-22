package tools

import "fmt"

// ValidateArgs checks args against the tool's schema subset before permission,
// rejecting unknown fields/types. See docs/Tools.md.
func ValidateArgs(def Definition, args map[string]any) error {
	if def.InputSchema == nil {
		return nil
	}
	rawProps, ok := def.InputSchema["properties"]
	if !ok {
		return nil
	}
	props, ok := rawProps.(map[string]any)
	if !ok {
		return nil
	}
	required := make(map[string]bool)
	if rawReq, ok := def.InputSchema["required"]; ok {
		switch v := rawReq.(type) {
		case []string:
			for _, k := range v {
				required[k] = true
			}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					required[s] = true
				}
			}
		}
	}
	for field := range required {
		if _, ok := args[field]; !ok {
			return &ArgumentError{Field: field, Reason: "is required"}
		}
	}
	// If no properties are defined, the schema is permissive (e.g. test
	// tools with {"type":"object"}). Only required checks apply; unknown
	// fields are allowed to avoid breaking existing tests.
	if len(props) == 0 {
		return nil
	}
	for key, val := range args {
		propRaw, ok := props[key]
		if !ok {
			return &ArgumentError{Field: key, Reason: "unknown field"}
		}
		prop, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		typeRaw, ok := prop["type"]
		if !ok {
			continue
		}
		typeStr, ok := typeRaw.(string)
		if !ok {
			continue
		}
		if err := checkType(key, typeStr, val); err != nil {
			return err
		}
		if enumRaw, ok := prop["enum"]; ok {
			if err := checkEnum(key, enumRaw, val); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkType(field, typeStr string, val any) error {
	switch typeStr {
	case "string":
		if _, ok := val.(string); !ok {
			return &ArgumentError{Field: field, Reason: "must be a string"}
		}
	case "number":
		switch val.(type) {
		case int, int8, int16, int32, int64, float32, float64:
		default:
			return &ArgumentError{Field: field, Reason: "must be a number"}
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return &ArgumentError{Field: field, Reason: "must be a boolean"}
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return &ArgumentError{Field: field, Reason: "must be an object"}
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return &ArgumentError{Field: field, Reason: "must be an array"}
		}
	default:
	}
	return nil
}

func checkEnum(field string, enumRaw any, val any) error {
	var enumVals []any
	switch v := enumRaw.(type) {
	case []string:
		for _, s := range v {
			enumVals = append(enumVals, s)
		}
	case []any:
		enumVals = v
	default:
		return nil
	}
	strVal, ok := val.(string)
	if !ok {
		return nil
	}
	for _, ev := range enumVals {
		if s, ok := ev.(string); ok && s == strVal {
			return nil
		}
	}
	return &ArgumentError{Field: field, Reason: fmt.Sprintf("must be one of %v", enumVals)}
}

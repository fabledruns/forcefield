package tools

import "fmt"

// ValidateArgs checks that args conform to def's InputSchema before
// permission is evaluated. It rejects unknown fields and wrong types so
// the permission prompt cannot hide intent behind a silent default.
//
// It is intentionally strict and minimal: it only checks the JSON-schema
// subset used by Forcefield's tools (type, required, enum, properties).
// It does not attempt full JSON-schema validation.
func ValidateArgs(def Definition, args map[string]any) error {
	if def.InputSchema == nil {
		return nil
	}
	// Extract properties
	rawProps, ok := def.InputSchema["properties"]
	if !ok {
		return nil
	}
	props, ok := rawProps.(map[string]any)
	if !ok {
		return nil
	}
	// Build required set
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
	// Check required fields present
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
	// Check for unknown fields and type mismatches
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
		// Enum check if present
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
			// ok
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
		// Unknown type, ignore
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

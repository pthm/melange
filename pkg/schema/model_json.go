package schema

import "encoding/json"

// MarshalModel serializes a parsed model to a compact JSON representation for
// storage (e.g. the model_json column of melange_migrations). It is the inverse
// of UnmarshalModel: MarshalModel followed by UnmarshalModel round-trips a model
// without loss.
//
// The JSON shape is the natural encoding of []TypeDefinition. All fields are
// exported and JSON-safe (no pointers, interfaces, or non-string map keys), so
// the round-trip is exact — see TestModelJSONRoundTrip.
func MarshalModel(types []TypeDefinition) ([]byte, error) {
	return json.Marshal(types)
}

// UnmarshalModel deserializes a model previously produced by MarshalModel.
// Empty or nil input yields a nil model with no error, matching the "no model
// recorded" case for databases migrated before model storage existed.
func UnmarshalModel(data []byte) ([]TypeDefinition, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var types []TypeDefinition
	if err := json.Unmarshal(data, &types); err != nil {
		return nil, err
	}
	return types, nil
}

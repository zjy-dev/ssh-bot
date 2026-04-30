package lark

import "encoding/json"

// Indirection so that card.go stays free of direct encoding/json references.
// Keeps the visible API of card.go small, and leaves room to swap in a
// faster encoder if ever needed (sonic, etc.).
func jsonMarshalIndirect(v any) ([]byte, error) {
	return json.Marshal(v)
}

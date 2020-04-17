package main

// p2v is the equivalent of referencing a pointer, but safely (no panic).
// Should be used for printing purposes (i.e. fmt.Printf(...))
func p2v(p interface{}) interface{} {
	switch value := p.(type) {
	case *string:
		if value == nil {
			return "<nil>"
		}
		return *value
	case *int64:
		if value == nil {
			return "<nil>"
		}
		return *value
	default:
		return value
	}
}

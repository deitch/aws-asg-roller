package main

func testStringEq(a, b []string) bool {

	// If one is nil, the other must also be nil.
	if (a == nil) != (b == nil) {
		return false
	}

	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type funcCounter struct {
	count []funcCounterImpl
}
type funcCounterImpl struct {
	name   string
	params []interface{}
}

func (f *funcCounter) add(name string, params ...interface{}) {
	f.count = append(f.count, funcCounterImpl{
		name:   name,
		params: params,
	})
}
func (f *funcCounter) last() (string, []interface{}) { //nolint:unused
	l := len(f.count)
	if l > 0 {
		return f.count[l-1].name, f.count[l-1].params
	}
	return "", nil
}
func (f *funcCounter) lastByName(name string) []interface{} { //nolint:unused
	var params []interface{}
	for _, call := range f.count {
		if call.name == name {
			params = call.params
		}
	}
	return params
}
func (f *funcCounter) filterByName(name string) []funcCounterImpl {
	ret := make([]funcCounterImpl, 0)
	for _, call := range f.count {
		if call.name == name {
			ret = append(ret, call)
		}
	}
	return ret
}

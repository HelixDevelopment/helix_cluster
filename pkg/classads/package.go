// Package classads provides ClassAd (classified advertisement) handling for Helix Cluster OS.
package classads

// ClassAd is a key-value attribute set.
type ClassAd map[string]interface{}

// New creates a new ClassAd.
func New() ClassAd {
	return make(ClassAd)
}

// Set sets an attribute.
func (c ClassAd) Set(key string, value interface{}) {
	c[key] = value
}

// Get gets an attribute.
func (c ClassAd) Get(key string) (interface{}, bool) {
	v, ok := c[key]
	return v, ok
}

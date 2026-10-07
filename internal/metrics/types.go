package metrics

import (
	"sort"
	"strings"
)

// MetricType identifies the kind of a metric.
type MetricType uint8

const (
	TypeCounter MetricType = iota + 1
	TypeGauge
	TypeHistogram
)

func (t MetricType) String() string {
	switch t {
	case TypeCounter:
		return "counter"
	case TypeGauge:
		return "gauge"
	case TypeHistogram:
		return "histogram"
	default:
		return "unknown"
	}
}

// Labels is an ordered set of label key/value pairs. The registry stores
// label sets in canonical form: keys sorted, unique, and syntax-checked.
// Construct values with NewLabels / NewLabelsFromPairs; the zero value is
// a valid empty label set.
type Labels struct {
	keys   []string
	values []string
}

// NewLabels builds a canonical (sorted, syntax-checked) label set from a map.
func NewLabels(kvs map[string]string) (Labels, error) {
	keys := make([]string, 0, len(kvs))
	for k := range kvs {
		if !IsValidLabelName(k) {
			return Labels{}, invalidLabelError(k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]string, len(keys))
	for i, k := range keys {
		values[i] = kvs[k]
	}
	return Labels{keys: keys, values: values}, nil
}

// NewLabelsFromPairs builds a canonical label set from ordered pairs and
// rejects duplicated keys (something a map input cannot express).
func NewLabelsFromPairs(pairs ...LabelPair) (Labels, error) {
	kvs := make(map[string]string, len(pairs))
	for _, p := range pairs {
		if !IsValidLabelName(p.Name) {
			return Labels{}, invalidLabelError(p.Name)
		}
		if _, dup := kvs[p.Name]; dup {
			return Labels{}, duplicateLabelError(p.Name)
		}
		kvs[p.Name] = p.Value
	}
	return NewLabels(kvs)
}

// MustLabels panics on construction errors; intended for static labels.
func MustLabels(kvs map[string]string) Labels {
	l, err := NewLabels(kvs)
	if err != nil {
		panic(err)
	}
	return l
}

// Len returns the number of label pairs.
func (l Labels) Len() int { return len(l.keys) }

// Key returns the canonical map key of the label set. Special characters in
// names/values are escaped so distinct label sets never collide.
func (l Labels) Key() string {
	var b strings.Builder
	for i := range l.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(escapeKey(l.keys[i]))
		b.WriteByte('=')
		b.WriteString(escapeKey(l.values[i]))
	}
	return b.String()
}

// Get returns the value for a label key.
func (l Labels) Get(key string) (string, bool) {
	i := sort.SearchStrings(l.keys, key)
	if i < len(l.keys) && l.keys[i] == key {
		return l.values[i], true
	}
	return "", false
}

// Pairs returns an ordered copy of the key/value pairs.
func (l Labels) Pairs() []LabelPair {
	out := make([]LabelPair, len(l.keys))
	for i := range l.keys {
		out[i] = LabelPair{Name: l.keys[i], Value: l.values[i]}
	}
	return out
}

func escapeKey(s string) string {
	if !strings.ContainsAny(s, `\,=`) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', ',', '=':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// LabelPair is one key/value pair in a Labels set.
type LabelPair struct {
	Name  string
	Value string
}

// IsValidMetricName reports whether name matches [a-zA-Z_:][a-zA-Z0-9_:]*.
func IsValidMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == ':':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// IsValidLabelName reports whether name matches [a-zA-Z_][a-zA-Z0-9_]*.
func IsValidLabelName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

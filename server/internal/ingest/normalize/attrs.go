package normalize

import "strconv"

// String returns the string at key, or nil if absent or non-string.
// Unlike Int64/Float64/Bool, String does not coerce.
func String(attrs map[string]any, key string) *string {
	v, ok := attrs[key]
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

// Int64 returns the integer at key, coercing from numeric representations.
// Coercion is required: live capture shows same field emitted as native int and
// as string (encoding/json decodes numbers as float64). Nil means absent, not zero.
func Int64(attrs map[string]any, key string) *int64 {
	v, ok := attrs[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case int64:
		return &t
	case int:
		i := int64(t)
		return &i
	case float64:
		i := int64(t)
		return &i
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return &n
		}
		// A vendor-emitted numeric string is occasionally not integral
		// (rare, but cheap to tolerate rather than reject per SPEC §0).
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			i := int64(f)
			return &i
		}
		return nil
	default:
		return nil
	}
}

// Float64 returns the float at key, coercing like Int64 does.
func Float64(attrs map[string]any, key string) *float64 {
	v, ok := attrs[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case float64:
		return &t
	case int64:
		f := float64(t)
		return &f
	case int:
		f := float64(t)
		return &f
	case string:
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return &f
		}
		return nil
	default:
		return nil
	}
}

// Bool returns the boolean at key, coercing from bool or string.
// Live capture shows both as native bool and as string.
func Bool(attrs map[string]any, key string) *bool {
	v, ok := attrs[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case bool:
		return &t
	case string:
		if b, err := strconv.ParseBool(t); err == nil {
			return &b
		}
		return nil
	default:
		return nil
	}
}

// Map returns the nested map at key. Reports absence via bool since {}
// and absent key are both valid and callers must distinguish them.
func Map(attrs map[string]any, key string) (map[string]any, bool) {
	v, ok := attrs[key]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// StringLike stringifies whichever scalar is present at key
// (string, int64, float64, bool in order), for columns with no fixed type.
// Returns nil only when key is absent or unrepresentable.
func StringLike(attrs map[string]any, key string) *string {
	if s := String(attrs, key); s != nil {
		return s
	}
	// Try Float64 before Int64: preserves fractional part for floats.
	if f := Float64(attrs, key); f != nil {
		s := strconv.FormatFloat(*f, 'f', -1, 64)
		return &s
	}
	if b := Bool(attrs, key); b != nil {
		s := strconv.FormatBool(*b)
		return &s
	}
	return nil
}

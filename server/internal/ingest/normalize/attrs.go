package normalize

import "strconv"

// String returns the string at key in attrs, or nil if the key is absent or
// holds a non-string value. Unlike Int64/Float64/Bool, String does not
// coerce: a caller asking for a string wants exactly what the vendor sent as
// text, and every OTel/JSON encoding already represents text as a native
// string, so there is no plausible "string in disguise" to recover.
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

// Int64 returns the integer at key, coercing from whatever numeric or
// numeric-looking representation is actually present. This is required, not
// defensive-programming paranoia: the live capture shows the same logical
// field (e.g. tool_result.duration_ms) emitted as a native OTel int in one
// event and an OTel *string* in another ("12"), and encoding/json decodes
// every JSON number as float64 regardless of the hook payload's intent. A
// nil return means "absent", never "zero" — a caller must not conflate the
// two (SPEC §1.3's promoted-column nullability depends on this).
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

// Float64 returns the float at key, coercing across the same set of
// representations Int64 does (see Int64's doc for why coercion is
// necessary, not optional).
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

// Bool returns the boolean at key. The capture shows booleans emitted both
// as native OTel bools (`is_plugin: false`) and as OTel strings
// (`safe_mode: "false"`, `success: "false"`), so a string is parsed with
// strconv.ParseBool rather than only accepted as a native bool.
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

// Map returns the nested map at key. It reports absence via the second
// return value rather than a nil map, because a present-but-empty map
// ({}) and an absent key are both legitimately expressible in JSON/OTLP
// kvlist attributes and callers (e.g. tool_result's tool_parameters lookup)
// need to tell them apart.
func Map(attrs map[string]any, key string) (map[string]any, bool) {
	v, ok := attrs[key]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// StringLike stringifies whichever scalar representation is present at key
// — string, then int64, then float64, then bool, in that order — for text
// columns whose vendor attribute has no fixed type (e.g. api_error's
// `status_code` fallback for `error_type`, which the capture never observed
// but SPEC §1.5.1 documents as an int-typed HTTP status). Returns nil only
// when the key is wholly absent or holds an unrepresentable type (a nested
// map or array).
func StringLike(attrs map[string]any, key string) *string {
	if s := String(attrs, key); s != nil {
		return s
	}
	// Float64 is tried before Int64 deliberately: Int64 truncates a native
	// float64 (2.5 → 2) so a genuine floating-point attribute value would
	// otherwise silently lose its fractional part when stringified here.
	// Float64.FormatFloat with precision -1 still renders a whole number
	// without a trailing ".0" (e.g. int64(42) → "42"), so trying Float64
	// first costs nothing for integer-valued attributes.
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

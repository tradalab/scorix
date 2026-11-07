package order

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
)

// Tags is a free-form label list stored as a JSON array in a text column. Writing trims, drops empty labels and
// repeats that differ only in case (the first spelling wins); reading a value that is not a JSON array of
// strings yields no tags rather than an error, so one bad row cannot fail a whole list.
type Tags []string

// Normalize trims, drops empty labels and case-insensitive repeats, keeping first spelling and order. Never nil.
func Normalize(in []string) Tags {
	out := make(Tags, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		if t == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

// Value writes the normalized list as JSON, "[]" when empty.
func (t Tags) Value() (driver.Value, error) {
	b, err := json.Marshal(Normalize(t))
	return string(b), err
}

// Scan reads a JSON array from a string or []byte column; NULL and malformed values read as no tags.
func (t *Tags) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*t = Tags{}
		return nil
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("order: cannot scan %T into Tags", src)
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		*t = Tags{}
		return nil
	}
	*t = Normalize(out)
	return nil
}

package confvalue

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var durationType = reflect.TypeOf(time.Duration(0))

// Assign sets fv, a settable scalar, time.Duration or []string, from v: a string as read
// from the environment or a tag, or a value YAML or JSON decoded.
func Assign(fv reflect.Value, v any) error {
	if fv.Type() == durationType {
		d, err := toDuration(v)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(toString(v))
	case reflect.Bool:
		b, err := toBool(v)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := toInt(v)
		if err != nil {
			return err
		}
		if fv.OverflowInt(n) {
			return fmt.Errorf("%v overflows %s", v, fv.Type())
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := toInt(v)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid unsigned value %v", v)
		}
		if fv.OverflowUint(uint64(n)) {
			return fmt.Errorf("%v overflows %s", v, fv.Type())
		}
		fv.SetUint(uint64(n))
	case reflect.Float32, reflect.Float64:
		fl, err := toFloat(v)
		if err != nil {
			return err
		}
		fv.SetFloat(fl)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element %s", fv.Type().Elem().Kind())
		}
		fv.Set(reflect.ValueOf(toStringSlice(v)))
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case string:
		return time.ParseDuration(strings.TrimSpace(x))
	case int:
		return time.Duration(x), nil
	case int64:
		return time.Duration(x), nil
	case float64:
		return time.Duration(int64(x)), nil
	default:
		return 0, fmt.Errorf("cannot read %v as duration", v)
	}
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case int:
		return x != 0, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "yes", "on", "y":
			return true, nil
		case "no", "off", "n":
			return false, nil
		}
		return strconv.ParseBool(strings.TrimSpace(x))
	default:
		return false, fmt.Errorf("cannot read %v as bool", v)
	}
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	default:
		return 0, fmt.Errorf("cannot read %v as int", v)
	}
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(x), 64)
	default:
		return 0, fmt.Errorf("cannot read %v as float", v)
	}
}

func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, toString(e))
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out
	default:
		return nil
	}
}

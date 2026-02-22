package extension

import (
	"context"
	"encoding/json"
	"strings"
)

func Decode[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func GetConfigPath(ctx context.Context, path string) (any, bool) {
	cfg, _ := Get[map[string]any](ctx, KeyConfig)

	if cfg == nil {
		return nil, false
	}

	if v, ok := cfg[path]; ok {
		return v, true
	}

	parts := strings.Split(path, ".")
	var cur any = cfg
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			// check map[interface{}]any (ex: from yaml), convert
			if mm, ok2 := cur.(map[interface{}]any); ok2 {
				// convert mm -> map[string]any on the fly
				tmp := make(map[string]any, len(mm))
				for kk, vv := range mm {
					if ks, ok := kk.(string); ok {
						tmp[ks] = vv
					}
				}
				m = tmp
			} else {
				return nil, false
			}
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

package extension

import "context"

type CtxKey string

const (
	KeyConfig CtxKey = "config"
	KeyApp    CtxKey = "app"
)

func Get[T any](ctx context.Context, key CtxKey) (T, bool) {
	v := ctx.Value(key)
	if cast, ok := v.(T); ok {
		return cast, true
	}
	var zero T
	return zero, false
}

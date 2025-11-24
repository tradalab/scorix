package extension

import "context"

type Extension interface {
	Name() string
	Init(ctx context.Context) error
	Stop(ctx context.Context) error
}

package extension

import (
	"context"

	"github.com/tradalab/scorix/kernel/core/messaging/command"
)

type Extension interface {
	Name() string
	Init(ctx context.Context, cmd *command.Command) error
	Stop(ctx context.Context) error
}

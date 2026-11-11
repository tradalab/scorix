package gate

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/kernel"
)

const MaxBody = 4 << 20

func Authenticate(ctx context.Context, auth kernel.Authenticator, call kernel.Call) (context.Context, error) {
	ctx, err := auth.Authenticate(ctx, call)
	if err != nil {
		return nil, err
	}
	p, ok := kernel.PrincipalFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the authenticator accepted the call without naming a principal"))
	}
	return kernel.Enter(ctx, call.Procedure, p), nil
}

func ReadBody(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBody {
		return nil, fmt.Errorf("the request body is over %d bytes", MaxBody)
	}
	return raw, nil
}

func Message(err error) string {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Message()
	}
	return err.Error()
}

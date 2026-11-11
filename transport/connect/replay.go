package connect

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
	"github.com/tradalab/scorix/transport/internal/gate"
)

func transient(code connect.Code) bool {
	switch code {
	case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDeadlineExceeded,
		connect.CodeCanceled, connect.CodeResourceExhausted, connect.CodeAborted, connect.CodeDataLoss:
		return true
	}
	return false
}

// Replay runs fn once per Idempotency-Key and keeps the outcome in store: a retry with the same
// request gets it, one with another request is invalid_argument, one while fn runs is aborted,
// and a transient failure is not kept, so its retry runs again.
func Replay[Req, Res any](ctx context.Context, store kernel.ReplayStore, procedure, key string, in *Req, fn func(context.Context, *Req) (*Res, error)) (*Res, error) {
	if len(key) > 255 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an Idempotency-Key is at most 255 bytes"))
	}
	req, ok := any(in).(proto.Message)
	out, outOK := any(new(Res)).(proto.Message)
	if !ok || !outOK {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s replays, but its request or reply is not a protobuf message", procedure))
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sum := sha256.Sum256(b)
	p, _ := kernel.PrincipalFrom(ctx)
	full := strings.Join([]string{strconv.Quote(string(p.Kind)), strconv.Quote(p.ID), strconv.Quote(procedure), strconv.Quote(key)}, " ")
	token, prior, err := store.Claim(ctx, full, sum)
	switch {
	case errors.Is(err, kernel.ErrInFlight):
		return nil, connect.NewError(connect.CodeAborted, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("replay store: %w", err))
	case prior != nil && prior.Sum != sum:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this Idempotency-Key was first used for a different request"))
	case prior != nil && prior.Code != 0:
		return nil, connect.NewError(connect.Code(prior.Code), errors.New(prior.Message))
	case prior != nil:
		if err := proto.Unmarshal(prior.Reply, out); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("replay store: %w", err))
		}
		return any(out).(*Res), nil
	}
	keep := context.WithoutCancel(ctx)
	settled := false
	defer func() {
		if !settled {
			logStore(keep, procedure, store.Drop(keep, full, token))
		}
	}()
	res, err := fn(ctx, in)
	settled = true
	if err != nil {
		if code := Code(err); transient(code) {
			logStore(keep, procedure, store.Drop(keep, full, token))
		} else {
			logStore(keep, procedure, store.Keep(keep, full, token, kernel.Replayed{Sum: sum, Code: int32(code), Message: gate.Message(err)}))
		}
		return nil, err
	}
	reply, err := proto.Marshal(any(res).(proto.Message))
	if err != nil {
		logStore(keep, procedure, store.Drop(keep, full, token))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	logStore(keep, procedure, store.Keep(keep, full, token, kernel.Replayed{Sum: sum, Reply: reply}))
	return res, nil
}

func logStore(ctx context.Context, procedure string, err error) {
	if err != nil {
		observe.Log(ctx).Warn("replay store: a retry with this key is aborted until its claim expires", "procedure", procedure, "error", err.Error())
	}
}

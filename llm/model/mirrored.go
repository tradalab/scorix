package model

import (
	"context"
	"errors"
	"net/http"

	"github.com/tradalab/scorix/fetch"
)

// Mirrored is one hub reached through several hosts with the same repos at the same revisions,
// such as huggingface.co and hf-mirror.com. Only for sources that build links from Repo, Revision
// and Path; one that hands out absolute links (OCI, GitHub, URL) sends every mirror to one host.
type Mirrored struct {
	hubs []Source
}

// The primary is asked first, the mirrors only when it cannot answer: its not found or gated
// is final, so no mirror can supply a repo the primary says does not exist.
func NewMirrored(primary Source, mirrors ...Source) *Mirrored {
	return &Mirrored{hubs: append([]Source{primary}, mirrors...)}
}

func (m *Mirrored) Name() string { return m.hubs[0].Name() }

func (m *Mirrored) Resolve(ctx context.Context, ref string) (*Snapshot, error) {
	var errs []error
	for i, hub := range m.hubs {
		snap, err := hub.Resolve(ctx, ref)
		if err == nil {
			return snap, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if i == 0 && (errors.Is(err, ErrNotFound) || errors.Is(err, ErrGated)) {
			return nil, err
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// Hosts opens one file from each hub in order; Store.Get hands them to fetch.Mirrors per
// download, so which host answered last is remembered for that download only.
func (m *Mirrored) Hosts(s *Snapshot, f File) []fetch.Opener {
	hosts := make([]fetch.Opener, len(m.hubs))
	for i, hub := range m.hubs {
		hosts[i] = func(ctx context.Context, offset int64) (*http.Response, error) {
			return hub.Open(ctx, s, f, offset)
		}
	}
	return hosts
}

func (m *Mirrored) Open(ctx context.Context, s *Snapshot, f File, offset int64) (*http.Response, error) {
	return fetch.Mirrors(m.Hosts(s, f)...)(ctx, offset)
}

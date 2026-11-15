package netx

import (
	"context"
	"errors"
	"strings"

	"github.com/tradalab/scorix/fault"
)

const (
	CodeHostKeyUnknown = "ssh_host_key_unknown"
	CodeHostKeyChanged = "ssh_host_key_changed"
)

// HostKeyFault turns a *HostKeyError anywhere in err's chain into a fault the shell can prompt with: code
// CodeHostKeyUnknown or CodeHostKeyChanged, details key (the authorized_keys line to pin) and fingerprint.
// It reports false for any other error; add app details such as the ssh config id with With.
func HostKeyFault(err error) (*fault.Error, bool) {
	var hk *HostKeyError
	if !errors.As(err, &hk) {
		return nil, false
	}
	code := CodeHostKeyUnknown
	if hk.Changed {
		code = CodeHostKeyChanged
	}
	return fault.Wrap(code, err).With("key", hk.AuthorizedKey()).With("fingerprint", hk.Fingerprint()), true
}

// IsHostKeyFault reports whether err is, or wraps, a fault made by HostKeyFault.
func IsHostKeyFault(err error) bool {
	code := fault.CodeOf(err)
	return code == CodeHostKeyUnknown || code == CodeHostKeyChanged
}

// VerifyHostKey is the check to run before reusing a client opened on this route: it returns the HostKeyFault
// when the bastion no longer shows the pinned key, and nil otherwise, network failures included, which the
// client reports on its own. While the SSH session is alive it costs no round trip; a nil Route returns nil.
func (r *Route) VerifyHostKey(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if fe, ok := HostKeyFault(r.Check(ctx)); ok {
		return fe
	}
	return nil
}

// PinAfterEdit is the host key to store when a saved ssh config is edited: a submitted key wins, an empty one
// keeps the stored pin, and moving the config to another host or port drops the old pin unless a key comes with
// the edit. A pin is a key, so one sent back for a new address stays valid only where that key answers.
func PinAfterEdit(stored, submitted string, sameHost bool) string {
	if s := strings.TrimSpace(submitted); s != "" {
		return s
	}
	if !sameHost {
		return ""
	}
	return stored
}

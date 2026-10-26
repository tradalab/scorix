package sqlx

import "errors"

// Android's seccomp on x86_64 only allows the *at file syscalls, and modernc's musl there makes the
// old ones (lstat, open), so the first open kills the process with SIGSYS. arm64 has no old ones.
func sqliteUnsupported() error {
	return errors.New("modernc sqlite cannot run on x86_64 Android, such as the emulator: use an arm64 device, or RegisterDriver(\"sqlite\", ...) with another driver")
}

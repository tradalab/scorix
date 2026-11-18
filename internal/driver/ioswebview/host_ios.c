//go:build ios && cgo

#include "host_ios.h"

static scorix_host_fn host;

void scorix_keep_host(scorix_host_fn fn) { host = fn; }

void scorix_to_host(int op, const void *data, int len) {
	if (host != 0) {
		host(op, data, len);
	}
}

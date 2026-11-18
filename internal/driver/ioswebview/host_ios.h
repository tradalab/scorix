//go:build ios && cgo

#ifndef SCORIX_IOSWEBVIEW_HOST_H
#define SCORIX_IOSWEBVIEW_HOST_H

typedef void (*scorix_host_fn)(int op, const void *data, int len);

void scorix_keep_host(scorix_host_fn fn);
void scorix_to_host(int op, const void *data, int len);

#endif

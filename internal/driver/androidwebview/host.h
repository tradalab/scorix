//go:build android && cgo

#ifndef SCORIX_ANDROIDWEBVIEW_HOST_H
#define SCORIX_ANDROIDWEBVIEW_HOST_H

void scorix_to_host(int op, const void *data, int len);
void scorix_log(const char *line);

#endif

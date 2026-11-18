#ifndef SCORIX_GO_H
#define SCORIX_GO_H

typedef void (*scorix_host_fn)(int op, const void *data, int len);

extern void scorixSetHost(scorix_host_fn fn);
extern void scorixStart(char *env, int n);
extern void scorixMessage(char *data, int n);
extern void scorixCallReply(char *data, int n);
extern void scorixRunTasks(void);
extern void scorixOpenURL(char *data, int n);
extern int scorixKeepsOut(char *data, int n, int clicked);
extern void scorixLifecycle(int code);
extern void *scorixRequest(char *data, int n, int *outLen);
extern void scorixFree(void *p);

#endif

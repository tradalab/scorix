//go:build android && cgo

#include <jni.h>
#include <stdlib.h>
#include <android/log.h>
#include "host.h"
#include "_cgo_export.h"

static JavaVM *g_vm;
static jobject g_host;
static jmethodID g_from_go;

JNIEXPORT jint JNI_OnLoad(JavaVM *vm, void *reserved) {
	g_vm = vm;
	return JNI_VERSION_1_6;
}

// Goroutines hop onto threads the JVM has never seen, so every call attaches first.
// Daemon attach: Go never lets its threads exit to detach, so the JVM must not wait on them.
static JNIEnv *thread_env(void) {
	JNIEnv *env;
	if ((*g_vm)->GetEnv(g_vm, (void **)&env, JNI_VERSION_1_6) == JNI_OK) {
		return env;
	}
	if ((*g_vm)->AttachCurrentThreadAsDaemon(g_vm, &env, NULL) != JNI_OK) {
		return NULL;
	}
	return env;
}

// Bytes, not NewStringUTF: JNI strings are modified UTF-8, which mangles any
// 4-byte UTF-8 sequence (emoji) in a JSON envelope.
static jbyteArray to_bytes(JNIEnv *env, const void *data, int len) {
	jbyteArray arr = (*env)->NewByteArray(env, len);
	if (arr != NULL && len > 0) {
		(*env)->SetByteArrayRegion(env, arr, 0, len, (const jbyte *)data);
	}
	return arr;
}

void scorix_to_host(int op, const void *data, int len) {
	JNIEnv *env = thread_env();
	if (env == NULL || g_host == NULL) {
		return;
	}
	jbyteArray arr = to_bytes(env, data, len);
	(*env)->CallVoidMethod(env, g_host, g_from_go, (jint)op, arr);
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionDescribe(env);
		(*env)->ExceptionClear(env);
	}
	(*env)->DeleteLocalRef(env, arr);
}

void scorix_log(const char *line) {
	__android_log_write(ANDROID_LOG_INFO, "scorix", line);
}

JNIEXPORT void JNICALL Java_dev_scorix_ScorixNative_start(JNIEnv *env, jclass cls, jobject host, jbyteArray environ) {
	if (g_host == NULL) {
		g_host = (*env)->NewGlobalRef(env, host);
		jclass hc = (*env)->GetObjectClass(env, host);
		g_from_go = (*env)->GetMethodID(env, hc, "fromGo", "(I[B)V");
		(*env)->DeleteLocalRef(env, hc);
	}
	jsize n = (*env)->GetArrayLength(env, environ);
	jbyte *p = (*env)->GetByteArrayElements(env, environ, NULL);
	scorixStart((char *)p, (int)n);
	(*env)->ReleaseByteArrayElements(env, environ, p, JNI_ABORT);
}

JNIEXPORT void JNICALL Java_dev_scorix_ScorixNative_onMessage(JNIEnv *env, jclass cls, jbyteArray msg) {
	jsize n = (*env)->GetArrayLength(env, msg);
	jbyte *p = (*env)->GetByteArrayElements(env, msg, NULL);
	scorixMessage((char *)p, (int)n);
	(*env)->ReleaseByteArrayElements(env, msg, p, JNI_ABORT);
}

JNIEXPORT jbyteArray JNICALL Java_dev_scorix_ScorixNative_handleRequest(JNIEnv *env, jclass cls, jbyteArray req) {
	jsize n = (*env)->GetArrayLength(env, req);
	jbyte *p = (*env)->GetByteArrayElements(env, req, NULL);
	int out_len = 0;
	void *out = scorixRequest((char *)p, (int)n, &out_len);
	(*env)->ReleaseByteArrayElements(env, req, p, JNI_ABORT);
	jbyteArray arr = to_bytes(env, out, out_len);
	free(out);
	return arr;
}

JNIEXPORT void JNICALL Java_dev_scorix_ScorixNative_callReply(JNIEnv *env, jclass cls, jbyteArray reply) {
	jsize n = (*env)->GetArrayLength(env, reply);
	jbyte *p = (*env)->GetByteArrayElements(env, reply, NULL);
	scorixCallReply((char *)p, (int)n);
	(*env)->ReleaseByteArrayElements(env, reply, p, JNI_ABORT);
}

JNIEXPORT void JNICALL Java_dev_scorix_ScorixNative_runTasks(JNIEnv *env, jclass cls) {
	scorixRunTasks();
}

JNIEXPORT void JNICALL Java_dev_scorix_ScorixNative_openURL(JNIEnv *env, jclass cls, jbyteArray url) {
	jsize n = (*env)->GetArrayLength(env, url);
	jbyte *p = (*env)->GetByteArrayElements(env, url, NULL);
	if (p == NULL) return;
	scorixOpenURL((char *)p, (int)n);
	(*env)->ReleaseByteArrayElements(env, url, p, JNI_ABORT);
}

JNIEXPORT void JNICALL Java_dev_scorix_ScorixNative_lifecycle(JNIEnv *env, jclass cls, jint event) {
	scorixLifecycle((int)event);
}

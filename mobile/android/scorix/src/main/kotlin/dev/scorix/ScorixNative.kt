package dev.scorix

// The Go side of these lives in internal/driver/androidwebview/jni_android.c.
internal object ScorixNative {
    const val FOREGROUND = 1
    const val BACKGROUND = 2
    const val LOW_MEMORY = 3

    @JvmStatic external fun start(host: ScorixHost, environ: ByteArray)
    @JvmStatic external fun onMessage(msg: ByteArray)
    @JvmStatic external fun handleRequest(req: ByteArray): ByteArray
    @JvmStatic external fun callReply(reply: ByteArray)
    @JvmStatic external fun runTasks()
    @JvmStatic external fun lifecycle(event: Int)
    @JvmStatic external fun openURL(url: ByteArray)
}

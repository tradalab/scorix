package dev.scorix

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.IntentSender
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.graphics.Color
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.WindowInsets
import android.view.WindowInsetsController
import android.view.WindowManager
import android.webkit.ConsoleMessage
import android.webkit.RenderProcessGoneDetail
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import org.json.JSONObject
import java.io.ByteArrayInputStream

private const val TAG = "scorix"

/**
 * Process-wide because the Go app outlives any one Activity; its window is rebuilt
 * into whichever Activity is current. Everything runs on the main thread.
 */
object ScorixHost {
    // Mirrors the op constants in internal/driver/androidwebview/driver_android.go.
    private const val OP_POST = 1
    private const val OP_WAKE = 2
    private const val OP_OPEN = 3
    private const val OP_EVAL = 4
    private const val OP_NAVIGATE = 5
    private const val OP_LOAD_HTML = 6
    private const val OP_SET_TITLE = 7
    private const val OP_FINISH = 8
    private const val OP_INIT_SCRIPT = 9
    private const val OP_CALL = 10
    private const val OP_CALL_CANCEL = 11
    private const val OP_SET_THEME = 12

    private class Spec(
        val origin: String,
        var title: String,
        val initScripts: MutableList<String>,
        val devTools: Boolean,
        val secure: Boolean,
    )

    private val main = Handler(Looper.getMainLooper())
    private var started = false
    private var activity: Activity? = null
    private var webView: WebView? = null
    private var frame: FrameLayout? = null
    // Kept here, not on the Activity, so a recreated one returns in the theme the page chose.
    private var theme = "system"
    private var configured = false
    private var spec: Spec? = null
    private var url: String? = null
    @Volatile private var replyProxy: JavaScriptReplyProxy? = null

    private val plugins = HashMap<String, ScorixPlugin>()
    private val calls = HashMap<Long, PluginCall>()
    private val results = HashMap<Int, (Int, Intent?) -> Unit>()
    private val granted = HashMap<Int, (Boolean) -> Unit>()
    private var nextRequest = 0x5c00
    // Calls showing a screen of the app's own over it. Go hears neither the stop under one nor the
    // start after: an app that locks in the background would lock while the user unlocks.
    internal var covering = 0
    private var hidden = false

    init {
        for (p in listOf(KeystorePlugin(), BiometricPlugin(), ClipboardPlugin(), DialogPlugin(), ScannerPlugin(), BrowserPlugin(), SharePlugin(), BluetoothPlugin(), NotificationPlugin())) plugins[p.name] = p
    }

    internal fun currentActivity(): Activity? = activity

    internal fun goBack(): Boolean {
        val wv = webView ?: return false
        if (!wv.canGoBack()) return false
        wv.goBack()
        return true
    }

    // For screenless work: the Activity may be gone when it runs, the application is not.
    @Volatile internal var appContext: Context? = null
        private set

    // An app plugin may replace a built-in one of the same name.
    internal fun register(list: List<ScorixPlugin>) {
        for (p in list) plugins[p.name] = p
    }

    // Made once per process: a recreated Activity must not build a second plugin over a sensor or a channel.
    private var generated: List<ScorixPlugin>? = null

    internal fun generated(context: Context) = generated ?: generatedPlugins(context).also { generated = it }

    // Same guard as lifecycle, and a throwing plugin must not take the Activity down with it.
    internal fun pluginLifecycle(activity: Activity, start: Boolean) {
        if (this.activity !== activity) return
        for (p in plugins.values) {
            try {
                if (start) p.onStart(activity) else p.onStop(activity)
            } catch (e: Exception) {
                Log.e(TAG, "plugin ${p.name} ${if (start) "onStart" else "onStop"} failed", e)
            }
        }
    }

    internal fun onMain() = Looper.myLooper() == Looper.getMainLooper()

    internal fun post(fn: () -> Unit) {
        main.post(fn)
    }

    internal fun reply(id: Long, reply: JSONObject) {
        calls.remove(id)
        ScorixNative.callReply(reply.toString().toByteArray(Charsets.UTF_8))
    }

    // Kept here so a picker that outlives a recreated Activity still finds its callback.
    internal fun startForResult(activity: Activity, intent: Intent, result: (Int, Intent?) -> Unit): Int {
        val code = nextRequest++
        results[code] = result
        // No app for the intent throws here; the caller rejects, so the callback must not linger.
        try {
            activity.startActivityForResult(intent, code)
        } catch (e: RuntimeException) {
            results.remove(code)
            throw e
        }
        return code
    }

    // Same table as startForResult: a system dialog (CompanionDeviceManager) answers through
    // onActivityResult too.
    internal fun startSenderForResult(activity: Activity, sender: IntentSender, result: (Int, Intent?) -> Unit): Int {
        val code = nextRequest++
        results[code] = result
        activity.startIntentSenderForResult(sender, code, null, 0, 0, 0)
        return code
    }

    internal fun requestPermissions(activity: Activity, names: Array<String>, result: (Boolean) -> Unit) {
        val code = nextRequest++
        granted[code] = result
        activity.requestPermissions(names, code)
    }

    internal fun onPermissionsResult(requestCode: Int, results: IntArray): Boolean {
        val cb = granted.remove(requestCode) ?: return false
        cb(results.isNotEmpty() && results.all { it == PackageManager.PERMISSION_GRANTED })
        return true
    }

    internal fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?): Boolean {
        val cb = results.remove(requestCode) ?: return false
        cb(resultCode, data)
        return true
    }

    private fun dispatchCall(text: String) {
        val o = JSONObject(text)
        val id = o.getLong("id")
        val name = o.getString("plugin")
        val method = o.getString("method")
        val call = PluginCall(id, name, method)
        calls[id] = call
        val plugin = plugins[name] ?: return call.reject("no_plugin", "no native plugin named $name")
        try {
            plugin.call(method, o.optJSONObject("args") ?: JSONObject(), call)
        } catch (e: Exception) {
            Log.e(TAG, "plugin $name.$method threw", e)
            call.reject("error", e.toString())
        }
    }

    internal fun attach(activity: Activity, library: String) {
        this.activity = activity
        appContext = activity.applicationContext
        if (!started) {
            started = true
            System.loadLibrary(library)
            ScorixNative.start(this, environ(activity))
        }
        // Created now rather than when Go opens its window: loading Chromium is
        // the slowest step of a cold start, and here it overlaps Go's own.
        newWebView(activity)
        build()
    }

    internal fun detach(activity: Activity) {
        if (this.activity !== activity) return
        webView?.destroy()
        webView = null
        frame = null
        replyProxy = null
        this.activity = null
    }

    // Go decides whether the scheme is one the app declared (app.protocols), as for argv.
    internal fun openIntent(activity: Activity, intent: Intent?) {
        if (this.activity !== activity || intent?.action != Intent.ACTION_VIEW) return
        // Reopened from Recents: Android replays the task's first intent, an already delivered link.
        if (intent.flags and Intent.FLAG_ACTIVITY_LAUNCHED_FROM_HISTORY != 0) return
        val url = intent.dataString ?: return
        ScorixNative.openURL(url.toByteArray(Charsets.UTF_8))
    }

    // A replaced Activity stops after its successor starts; its late onStop would wrongly report background.
    internal fun lifecycle(activity: Activity, event: Int) {
        if (this.activity !== activity) return
        when {
            event == ScorixNative.BACKGROUND && covering > 0 -> hidden = true
            event == ScorixNative.FOREGROUND && hidden -> hidden = false
            else -> ScorixNative.lifecycle(event)
        }
    }

    // Go has no Android Context: its data, config and temp dirs come from here.
    private fun environ(activity: Activity): ByteArray {
        val files = activity.filesDir.absolutePath
        val cache = activity.cacheDir.absolutePath
        val vars = mutableListOf(
            "HOME=$files",
            "XDG_DATA_HOME=$files",
            "XDG_CONFIG_HOME=$files/config",
            "XDG_CACHE_HOME=$cache",
            "TMPDIR=$cache",
        )
        devUrl(activity)?.let { vars += "SCORIX_DEV_URL=$it" }
        return vars.joinToString("\n").toByteArray()
    }

    // Debug only: a launcher Activity takes intents from any app, so in release
    // anyone could aim the bridge at their own page.
    private fun devUrl(activity: Activity): String? {
        if (activity.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE == 0) return null
        return activity.intent?.getStringExtra("scorix.devUrl")?.takeIf { it.isNotEmpty() }
    }

    /** Called by Go on any thread; posting keeps the order Go sent in. */
    @Suppress("unused")
    fun fromGo(op: Int, data: ByteArray) {
        if (op == OP_POST) {
            val text = String(data, Charsets.UTF_8)
            main.post { replyProxy?.postMessage(text) }
            return
        }
        main.post { handle(op, data) }
    }

    private fun handle(op: Int, data: ByteArray) {
        val text = String(data, Charsets.UTF_8)
        when (op) {
            OP_WAKE -> ScorixNative.runTasks()
            OP_OPEN -> {
                val o = JSONObject(text)
                spec = Spec(
                    origin = o.getString("origin"),
                    title = o.optString("title"),
                    initScripts = mutableListOf(o.optString("initScript")),
                    devTools = o.optBoolean("devTools"),
                    secure = o.optBoolean("secure"),
                )
                theme = o.optString("theme").ifEmpty { "system" }
                activity?.let { applyTheme(it) }
                url = o.getString("url")
                build()
            }
            OP_EVAL -> webView?.evaluateJavascript(text, null)
            OP_NAVIGATE -> {
                url = text
                webView?.loadUrl(text)
            }
            OP_LOAD_HTML -> spec?.let { webView?.loadDataWithBaseURL("${it.origin}/", text, "text/html", "utf-8", null) }
            OP_SET_TITLE -> {
                spec?.title = text
                activity?.title = text
            }
            OP_FINISH -> activity?.finishAndRemoveTask()
            OP_SET_THEME -> {
                theme = text
                activity?.let { applyTheme(it) }
            }
            OP_CALL -> dispatchCall(text)
            OP_CALL_CANCEL -> calls.remove(JSONObject(text).getLong("id"))?.cancel()
            OP_INIT_SCRIPT -> spec?.let { s ->
                s.initScripts += text
                if (configured) webView?.let { WebViewCompat.addDocumentStartJavaScript(it, text, setOf(s.origin)) }
            }
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun newWebView(a: Activity) {
        webView?.destroy()
        val wv = WebView(a)
        wv.settings.javaScriptEnabled = true
        wv.settings.domStorageEnabled = true
        wv.webViewClient = Client()
        // The WebView ignores its own padding and still draws under the bars, so pad a parent.
        val f = FrameLayout(a)
        f.setOnApplyWindowInsetsListener { v, insets -> padForSystemBars(v, insets) }
        f.addView(wv, FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT))
        a.setContentView(f)
        frame = f
        applyTheme(a)
        webView = wv
        configured = false
    }

    // Once per WebView, when Go has opened its window and an Activity is up, in either order.
    private fun build() {
        val a = activity ?: return
        val wv = webView ?: return
        val s = spec ?: return
        val start = url ?: return
        if (configured) return
        configured = true
        check(
            WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER) &&
                WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT),
        ) { "scorix: this WebView is too old (needs WebMessageListener and document-start scripts); update Android System WebView" }

        if (s.secure) a.window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        a.title = s.title
        WebView.setWebContentsDebuggingEnabled(s.devTools)
        if (s.devTools) wv.webChromeClient = Console()

        // Only the app's own origin gets the bridge object and the init
        // scripts, so a page navigated elsewhere cannot call into Go.
        val origins = setOf(s.origin)
        WebViewCompat.addWebMessageListener(wv, "scorixAndroid", origins) { _, message, _, isMainFrame, proxy ->
            if (isMainFrame) {
                replyProxy = proxy
                val msg = message.data
                if (!msg.isNullOrEmpty()) ScorixNative.onMessage(msg.toByteArray(Charsets.UTF_8))
            }
        }
        for (js in s.initScripts) {
            if (js.isNotEmpty()) WebViewCompat.addDocumentStartJavaScript(wv, js, origins)
        }
        wv.loadUrl(start)
    }

    /** uiMode is in configChanges so a theme switch keeps the page; the bars are restyled here. */
    internal fun configurationChanged(a: Activity) {
        if (a === activity && theme == "system") applyTheme(a)
    }

    private fun isNight(a: Activity) = when (theme) {
        "dark" -> true
        "light" -> false
        else -> (a.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
    }

    // The frame shows under the transparent bars of edge-to-edge, and the bar
    // icons default to white: invisible on the light frame of a light theme.
    private fun applyTheme(a: Activity) {
        val night = isNight(a)
        frame?.setBackgroundColor(if (night) Color.BLACK else Color.WHITE)
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return
        val light = WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS
        a.window.insetsController?.setSystemBarsAppearance(if (night) 0 else light, light)
    }

    // Consumed after padding: passed on, a recent WebView also maps them to env(safe-area-inset-*)
    // and the page clears the bars twice. The keyboard inset still passes.
    private fun padForSystemBars(v: android.view.View, insets: WindowInsets): WindowInsets {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            val types = WindowInsets.Type.systemBars() or WindowInsets.Type.displayCutout()
            val bars = insets.getInsets(types)
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            return WindowInsets.Builder(insets).setInsets(types, android.graphics.Insets.NONE).build()
        }
        @Suppress("DEPRECATION")
        v.setPadding(insets.systemWindowInsetLeft, insets.systemWindowInsetTop, insets.systemWindowInsetRight, insets.systemWindowInsetBottom)
        @Suppress("DEPRECATION")
        return insets.consumeSystemWindowInsets()
    }

    private class Client : WebViewClient() {
        // Runs on a WebView worker thread, so the synchronous call into Go does not block the UI.
        override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
            if (request.url.host?.endsWith(".localhost") != true) return null
            val head = StringBuilder("${request.method} ${request.url}\n")
            for ((k, v) in request.requestHeaders) head.append(k).append(": ").append(v).append('\n')
            return toResponse(ScorixNative.handleRequest(head.toString().toByteArray(Charsets.UTF_8)))
        }

        // The renderer is a separate process the system kills under memory
        // pressure; returning false would take the whole app down with it.
        override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
            val a = activity
            if (view === webView && a != null) {
                newWebView(a)
                build()
            }
            return true
        }
    }

    private class Console : WebChromeClient() {
        override fun onConsoleMessage(m: ConsoleMessage): Boolean {
            Log.i(TAG, "console: ${m.message()}")
            return true
        }
    }
}

// Reads what packResponse in driver_android.go writes.
private fun toResponse(packed: ByteArray): WebResourceResponse {
    var pos = packed.indexOf('\n'.code.toByte())
    val statusLine = String(packed, 0, pos, Charsets.US_ASCII)
    val status = statusLine.substringBefore(' ').toInt()
    val reason = statusLine.substringAfter(' ').ifEmpty { "Unknown" }
    val headers = HashMap<String, String>()
    var contentType = "application/octet-stream"
    while (true) {
        val start = pos + 1
        val end = packed.indexOfFrom('\n'.code.toByte(), start)
        if (end <= start) {
            pos = start
            break
        }
        val line = String(packed, start, end - start, Charsets.UTF_8)
        val k = line.substringBefore(": ")
        val v = line.substringAfter(": ")
        if (k.equals("Content-Type", ignoreCase = true)) contentType = v else headers[k] = v
        pos = end
    }
    val mime = contentType.substringBefore(';').trim()
    val charset = contentType.substringAfter("charset=", "").trim().ifEmpty { null }
    val body = ByteArrayInputStream(packed, pos + 1, packed.size - pos - 1)
    return WebResourceResponse(mime, charset, status, reason, headers, body)
}

private fun ByteArray.indexOfFrom(b: Byte, from: Int): Int {
    for (i in from until size) if (this[i] == b) return i
    return -1
}

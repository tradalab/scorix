package dev.scorix

import android.app.Activity
import android.content.Context
import java.lang.reflect.InvocationTargetException
import android.content.Intent
import android.content.IntentSender
import org.json.JSONObject

/**
 * [call] runs on the main thread and must end every [PluginCall] with resolve or
 * reject, now or later. Plugins generated from `@native` services register themselves;
 * others are added by overriding [ScorixActivity.plugins].
 */
interface ScorixPlugin {
    val name: String

    fun call(method: String, args: JSONObject, call: PluginCall)

    /** The app came on screen; a plugin holding a sensor or a listener takes it back here. */
    fun onStart(activity: Activity) {}

    /** The app left the screen; release what [onStart] took, the process may live on for hours. */
    fun onStop(activity: Activity) {}
}

/**
 * The typed side of a [PluginCall], for plugins extending a base that `scorix generate proto`
 * writes from an `@native` service. [encode] turns [T] into what [PluginCall.resolve] takes.
 */
class Reply<T>(val call: PluginCall, private val encode: (T) -> Any?) {
    fun resolve(value: T) = call.resolve(encode(value))

    fun reject(code: String, message: String) = call.reject(code, message)
}

// Written by `scorix generate proto` into the app; absent when the proto has no @native service.
private const val GENERATED_PLUGINS = "dev.scorix.app.ScorixPlugins"

internal fun generatedPlugins(context: Context): List<ScorixPlugin> = try {
    @Suppress("UNCHECKED_CAST")
    Class.forName(GENERATED_PLUGINS).getMethod("create", Context::class.java).invoke(null, context) as List<ScorixPlugin>
} catch (e: ClassNotFoundException) {
    emptyList()
} catch (e: InvocationTargetException) {
    // A plugin constructor threw; its own exception is the one worth a stack trace.
    throw e.targetException
}

class PluginCall internal constructor(
    private val id: Long,
    private val plugin: String,
    private val method: String,
) {
    private var done = false
    private var onCancel: (() -> Unit)? = null

    /** The Activity on screen, or null between two of them. */
    val activity: Activity? get() = ScorixHost.currentActivity()

    /** [data] is a JSONObject, JSONArray, String, Number, Boolean or null. */
    @JvmOverloads
    fun resolve(data: Any? = null) = finish(JSONObject().put("ok", true).put("data", data ?: JSONObject.NULL))

    /** [code] is the stable reason Go branches on: "canceled", "unavailable"... */
    fun reject(code: String, message: String) = finish(JSONObject().put("ok", false).put("code", code).put("error", message))

    /** Runs when Go abandons the call (its context ended); no reply is sent after it. */
    fun onCancel(fn: () -> Unit) {
        onCancel = fn
    }

    /** If Go abandons the call first, the started Activity is closed. */
    fun startForResult(intent: Intent, result: (resultCode: Int, data: Intent?) -> Unit) {
        val a = activity ?: return reject("no_activity", "$plugin.$method needs an Activity on screen")
        val code = ScorixHost.startForResult(a, intent, result)
        if (onCancel == null) onCancel = { ScorixHost.currentActivity()?.finishActivity(code) }
    }

    /** For a system dialog that hands back an IntentSender, such as CompanionDeviceManager. */
    fun startSenderForResult(sender: IntentSender, result: (resultCode: Int, data: Intent?) -> Unit) {
        val a = activity ?: return reject("no_activity", "$plugin.$method needs an Activity on screen")
        val code = ScorixHost.startSenderForResult(a, sender, result)
        if (onCancel == null) onCancel = { ScorixHost.currentActivity()?.finishActivity(code) }
    }

    /** Asks for runtime permissions; [result] is true only when every one was granted. */
    fun requestPermissions(names: Array<String>, result: (granted: Boolean) -> Unit) {
        val a = activity ?: return reject("no_activity", "$plugin.$method needs an Activity on screen")
        ScorixHost.requestPermissions(a, names, result)
    }

    internal fun cancel() {
        if (done) return
        done = true
        onCancel?.invoke()
    }

    // Plugins answer from worker threads too; done, cancel and the call table all live on the main thread.
    private fun finish(reply: JSONObject) {
        if (!ScorixHost.onMain()) return ScorixHost.post { finish(reply) }
        if (done) return
        done = true
        ScorixHost.reply(id, reply.put("id", id))
    }
}

package dev.scorix

import android.app.Activity
import android.content.Intent
import org.json.JSONObject

/**
 * [call] runs on the main thread and must end every [PluginCall] with resolve or
 * reject, now or later. Register app plugins by overriding [ScorixActivity.plugins].
 */
interface ScorixPlugin {
    val name: String

    fun call(method: String, args: JSONObject, call: PluginCall)
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

    internal fun cancel() {
        if (done) return
        done = true
        onCancel?.invoke()
    }

    private fun finish(reply: JSONObject) {
        if (done) return
        done = true
        ScorixHost.reply(id, reply.put("id", id))
    }
}

package dev.scorix

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PersistableBundle
import org.json.JSONObject

internal class ClipboardPlugin : ScorixPlugin {
    override val name = "clipboard"
    private val main = Handler(Looper.getMainLooper())

    override fun call(method: String, args: JSONObject, call: PluginCall) {
        val a = call.activity ?: return call.reject("no_activity", "the clipboard is only readable from the foreground")
        val cm = a.getSystemService(ClipboardManager::class.java)
        when (method) {
            "read" -> {
                val text = cm.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(a)?.toString() ?: ""
                call.resolve(JSONObject().put("text", text))
            }
            "write" -> {
                write(a, cm, args.getString("text"), args.optBoolean("sensitive"), args.optLong("clearAfterMs"))
                call.resolve()
            }
            else -> call.reject("no_method", "clipboard has no method $method")
        }
    }

    private fun write(c: Context, cm: ClipboardManager, text: String, sensitive: Boolean, clearAfterMs: Long) {
        // A unique label lets the timed clear spare whatever the user copied since.
        val label = "scorix:" + System.nanoTime()
        val clip = ClipData.newPlainText(label, text)
        if (sensitive) {
            // Hides the text from the copy preview and keyboard; read from API 33, ignored before.
            clip.description.extras = PersistableBundle().apply {
                putBoolean("android.content.extra.IS_SENSITIVE", true)
            }
        }
        cm.setPrimaryClip(clip)
        if (clearAfterMs <= 0) return
        main.postDelayed({
            // From API 29 a background app cannot read the clip; clear anyway, a leaked secret is worse.
            val now = cm.primaryClipDescription?.label?.toString()
            if (now != null && now != label) return@postDelayed
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) cm.clearPrimaryClip() else cm.setPrimaryClip(ClipData.newPlainText("", ""))
        }, clearAfterMs)
    }
}

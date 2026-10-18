package dev.scorix

import android.content.Intent
import org.json.JSONObject

internal class SharePlugin : ScorixPlugin {
    override val name = "share"

    override fun call(method: String, args: JSONObject, call: PluginCall) {
        if (method != "share") return call.reject("no_method", "share has no method $method")
        val a = call.activity ?: return call.reject("no_activity", "the share sheet needs an Activity on screen")
        // EXTRA_TEXT is the one field every target reads, so the link rides in it.
        val text = listOf(args.optString("text"), args.optString("url")).filter { it.isNotEmpty() }.joinToString("\n")
        val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text)
        args.optString("title").takeIf { it.isNotEmpty() }?.let { send.putExtra(Intent.EXTRA_SUBJECT, it) }
        a.startActivity(Intent.createChooser(send, args.optString("dialogTitle").ifEmpty { null }))
        call.resolve()
    }
}

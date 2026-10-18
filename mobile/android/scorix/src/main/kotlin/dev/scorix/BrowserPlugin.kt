package dev.scorix

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import org.json.JSONObject

internal class BrowserPlugin : ScorixPlugin {
    override val name = "browser"

    override fun call(method: String, args: JSONObject, call: PluginCall) {
        if (method != "open") return call.reject("no_method", "browser has no method $method")
        val a = call.activity ?: return call.reject("no_activity", "opening a url needs an Activity on screen")
        val url = args.getString("url")
        // Started blind instead of checked with resolveActivity: from API 30 that
        // check sees no other app unless the manifest lists it under <queries>.
        try {
            a.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
            call.resolve()
        } catch (e: ActivityNotFoundException) {
            call.reject("unavailable", "no app can open $url")
        }
    }
}

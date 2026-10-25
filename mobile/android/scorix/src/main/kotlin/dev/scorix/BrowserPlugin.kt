package dev.scorix

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri

internal class BrowserPlugin : BrowserPluginBase() {
    override fun open(req: BrowserUrl, reply: Reply<Empty>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "opening a url needs an Activity on screen")
        // Started blind instead of checked with resolveActivity: from API 30 that
        // check sees no other app unless the manifest lists it under <queries>.
        try {
            a.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(req.url)))
            reply.resolve(Empty())
        } catch (e: ActivityNotFoundException) {
            reply.reject("unavailable", "no app can open ${req.url}")
        }
    }
}

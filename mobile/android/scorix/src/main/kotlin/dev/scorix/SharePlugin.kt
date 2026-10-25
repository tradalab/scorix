package dev.scorix

import android.content.Intent

internal class SharePlugin : SharePluginBase() {
    override fun share(req: ShareContent, reply: Reply<Empty>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "the share sheet needs an Activity on screen")
        // EXTRA_TEXT is the one field every target reads, so the link rides in it.
        val text = listOf(req.text, req.url).filter { it.isNotEmpty() }.joinToString("\n")
        val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text)
        req.title.takeIf { it.isNotEmpty() }?.let { send.putExtra(Intent.EXTRA_SUBJECT, it) }
        a.startActivity(Intent.createChooser(send, req.dialogTitle.ifEmpty { null }))
        reply.resolve(Empty())
    }
}

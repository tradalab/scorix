package dev.scorix

import android.content.ClipData
import android.content.Intent
import java.io.File

internal class SharePlugin : SharePluginBase() {
    override fun share(req: ShareContent, reply: Reply<Empty>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "the share sheet needs an Activity on screen")
        // EXTRA_TEXT is the one field every target reads, so the link rides in it.
        val text = listOf(req.text, req.url).filter { it.isNotEmpty() }.joinToString("\n")
        val send = Intent(Intent.ACTION_SEND).setType("text/plain")
        if (text.isNotEmpty()) send.putExtra(Intent.EXTRA_TEXT, text)
        req.title.takeIf { it.isNotEmpty() }?.let { send.putExtra(Intent.EXTRA_SUBJECT, it) }
        if (req.data.isNotEmpty()) {
            // One file at a time: a new share replaces whatever the last target may still be reading.
            val dir = ShareProvider.dir(a).apply { deleteRecursively(); mkdirs() }
            File(dir, req.fileName).writeBytes(req.data)
            val uri = ShareProvider.uri(a, req.fileName)
            send.setType(req.mime).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            // The chooser passes the read grant on only for URIs it finds in ClipData.
            send.clipData = ClipData.newRawUri(req.fileName, uri)
        }
        a.startActivity(Intent.createChooser(send, req.dialogTitle.ifEmpty { null }))
        reply.resolve(Empty())
    }
}

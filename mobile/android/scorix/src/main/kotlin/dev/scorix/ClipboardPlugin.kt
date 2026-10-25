package dev.scorix

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PersistableBundle

internal class ClipboardPlugin : ClipboardPluginBase() {
    private val main = Handler(Looper.getMainLooper())

    override fun read(req: Empty, reply: Reply<PlainText>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "the clipboard is only readable from the foreground")
        val cm = a.getSystemService(ClipboardManager::class.java)
        reply.resolve(PlainText(text = cm.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(a)?.toString() ?: ""))
    }

    override fun write(req: ClipWrite, reply: Reply<Empty>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "the clipboard is only readable from the foreground")
        write(a, a.getSystemService(ClipboardManager::class.java), req.text, req.sensitive, req.clearAfterMs)
        reply.resolve(Empty())
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

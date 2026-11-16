package dev.scorix

import android.graphics.Bitmap
import android.graphics.Canvas
import java.io.ByteArrayOutputStream

internal class CapturePlugin : CapturePluginBase() {
    override fun page(req: Empty, reply: Reply<PageImage>) {
        val wv = ScorixHost.currentWebView() ?: return reply.reject("unavailable", "no page on screen")
        if (wv.width == 0 || wv.height == 0) return reply.reject("unavailable", "the page is not laid out yet")
        val bmp = Bitmap.createBitmap(wv.width, wv.height, Bitmap.Config.ARGB_8888)
        wv.draw(Canvas(bmp))
        Thread {
            val out = ByteArrayOutputStream()
            bmp.compress(Bitmap.CompressFormat.PNG, 100, out)
            bmp.recycle()
            reply.resolve(PageImage(png = out.toByteArray()))
        }.start()
    }
}

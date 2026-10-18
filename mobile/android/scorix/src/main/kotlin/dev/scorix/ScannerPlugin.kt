package dev.scorix

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.LuminanceSource
import com.google.zxing.NotFoundException
import com.google.zxing.RGBLuminanceSource
import com.google.zxing.common.GlobalHistogramBinarizer
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader
import org.json.JSONObject

internal class ScannerPlugin : ScorixPlugin {
    override val name = "scanner"

    override fun call(method: String, args: JSONObject, call: PluginCall) {
        when (method) {
            "scanQR" -> scanCamera(call)
            "scanImage" -> scanImage(call)
            else -> call.reject("no_method", "scanner has no method $method")
        }
    }

    private fun scanCamera(call: PluginCall) {
        val a = call.activity ?: return call.reject("no_activity", "scanning needs an Activity on screen")
        val declared = a.packageManager.getPackageInfo(a.packageName, PackageManager.GET_PERMISSIONS)
            .requestedPermissions?.contains(Manifest.permission.CAMERA) == true
        if (!declared) {
            return call.reject(
                "unavailable",
                "the app does not declare the camera: add <uses-permission android:name=\"android.permission.CAMERA\" /> to its AndroidManifest.xml",
            )
        }
        call.startForResult(Intent(a, ScanActivity::class.java)) { code, data ->
            when (code) {
                Activity.RESULT_OK -> call.resolve(JSONObject().put("text", data?.getStringExtra(ScanActivity.EXTRA_TEXT) ?: ""))
                ScanActivity.RESULT_DENIED -> call.reject("denied", "the user did not allow the camera")
                ScanActivity.RESULT_FAILED -> call.reject("failed", "the camera could not be opened")
                else -> call.reject("canceled", "the user canceled")
            }
        }
    }

    // A QR already on this phone cannot be pointed at its own camera. The photo picker needs
    // no storage permission; before Android 13 the generic image picker stands in.
    private fun scanImage(call: PluginCall) {
        val pick = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            Intent(MediaStore.ACTION_PICK_IMAGES)
        } else {
            Intent(Intent.ACTION_GET_CONTENT).setType("image/*").addCategory(Intent.CATEGORY_OPENABLE)
        }
        call.startForResult(pick) { code, data ->
            val uri = data?.data
            val a = call.activity
            if (code != Activity.RESULT_OK || uri == null || a == null) return@startForResult call.reject("canceled", "no image picked")
            // Off the main thread: decoding a 12 MP photo visibly stalls the UI.
            val main = Handler(Looper.getMainLooper())
            Thread {
                val text = try {
                    decodeImage(a, uri)
                } catch (e: Exception) {
                    main.post { call.reject("failed", "cannot read the image: ${e.message}") }
                    return@Thread
                }
                main.post {
                    if (text == null) call.reject("not_found", "no QR code in that image")
                    else call.resolve(JSONObject().put("text", text))
                }
            }.start()
        }
    }

    private fun decodeImage(a: Activity, uri: Uri): String? {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        a.contentResolver.openInputStream(uri)?.use { BitmapFactory.decodeStream(it, null, bounds) }
        // About 2000 px on the long side reads any on-screen code and bounds the allocation.
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= MAX_SIDE) sample *= 2
        val bmp = a.contentResolver.openInputStream(uri)?.use {
            BitmapFactory.decodeStream(it, null, BitmapFactory.Options().apply { inSampleSize = sample })
        } ?: throw IllegalArgumentException("not an image")
        val px = IntArray(bmp.width * bmp.height)
        bmp.getPixels(px, 0, bmp.width, 0, 0, bmp.width, bmp.height)
        val src = RGBLuminanceSource(bmp.width, bmp.height, px)
        bmp.recycle()
        // Screenshots of dark-mode pages carry light-on-dark codes, and the
        // two binarizers fail on different images; try each combination.
        for (s in listOf<LuminanceSource>(src, src.invert())) {
            for (bin in listOf(HybridBinarizer(s), GlobalHistogramBinarizer(s))) {
                try {
                    return QRCodeReader().decode(BinaryBitmap(bin), HINTS).text
                } catch (_: NotFoundException) {
                } catch (_: com.google.zxing.ChecksumException) {
                } catch (_: com.google.zxing.FormatException) {
                }
            }
        }
        return null
    }

    private companion object {
        const val MAX_SIDE = 2000
        val HINTS = mapOf(DecodeHintType.TRY_HARDER to true)
    }
}

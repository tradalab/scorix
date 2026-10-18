package dev.scorix

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.ImageFormat
import android.graphics.Matrix
import android.graphics.SurfaceTexture
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.CaptureRequest
import android.media.ImageReader
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.util.Size
import android.view.Surface
import android.view.TextureView
import android.view.WindowManager
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader

/** Locked to portrait so the preview needs no rotation handling. */
class ScanActivity : Activity() {
    private lateinit var texture: TextureView
    private var thread: HandlerThread? = null
    private var bg: Handler? = null
    private var camera: CameraDevice? = null
    private var session: CameraCaptureSession? = null
    private var reader: ImageReader? = null
    private var asked = false
    @Volatile private var done = false
    private val qr = QRCodeReader()
    private val hints = mapOf(DecodeHintType.TRY_HARDER to true)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        texture = TextureView(this)
        setContentView(texture)
    }

    override fun onResume() {
        super.onResume()
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            if (!asked) {
                asked = true
                requestPermissions(arrayOf(Manifest.permission.CAMERA), 1)
            }
            return
        }
        thread = HandlerThread("scorix-scan").also { it.start() }
        bg = Handler(thread!!.looper)
        if (texture.isAvailable) open() else texture.surfaceTextureListener = object : TextureView.SurfaceTextureListener {
            override fun onSurfaceTextureAvailable(st: SurfaceTexture, w: Int, h: Int) = open()
            override fun onSurfaceTextureSizeChanged(st: SurfaceTexture, w: Int, h: Int) {}
            override fun onSurfaceTextureDestroyed(st: SurfaceTexture) = true
            override fun onSurfaceTextureUpdated(st: SurfaceTexture) {}
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, results: IntArray) {
        if (results.firstOrNull() != PackageManager.PERMISSION_GRANTED) {
            setResult(RESULT_DENIED)
            finish()
        }
    }

    // Drain the decoder thread before closing the reader: reading from a closed reader throws.
    override fun onPause() {
        session?.close()
        camera?.close()
        thread?.quitSafely()
        thread?.join()
        reader?.close()
        session = null
        camera = null
        thread = null
        reader = null
        super.onPause()
    }

    @Suppress("MissingPermission", "DEPRECATION")
    private fun open() {
        val cm = getSystemService(CameraManager::class.java)
        val id = cm.cameraIdList.firstOrNull {
            cm.getCameraCharacteristics(it).get(CameraCharacteristics.LENS_FACING) == CameraCharacteristics.LENS_FACING_BACK
        } ?: cm.cameraIdList.firstOrNull() ?: return fail()
        val map = cm.getCameraCharacteristics(id).get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP) ?: return fail()
        // Big enough to read a small code, small enough to decode every frame.
        val size = map.getOutputSizes(ImageFormat.YUV_420_888)
            .filter { it.width <= 1280 && it.height <= 1280 }
            .maxByOrNull { it.width * it.height } ?: Size(640, 480)

        val r = ImageReader.newInstance(size.width, size.height, ImageFormat.YUV_420_888, 2)
        r.setOnImageAvailableListener({ decode(it) }, bg)
        reader = r
        val st = texture.surfaceTexture ?: return fail()
        st.setDefaultBufferSize(size.width, size.height)
        val preview = Surface(st)
        fitPreview(size)

        cm.openCamera(id, object : CameraDevice.StateCallback() {
            override fun onOpened(cam: CameraDevice) {
                camera = cam
                cam.createCaptureSession(listOf(preview, r.surface), object : CameraCaptureSession.StateCallback() {
                    override fun onConfigured(s: CameraCaptureSession) {
                        session = s
                        val req = cam.createCaptureRequest(CameraDevice.TEMPLATE_PREVIEW)
                        req.addTarget(preview)
                        req.addTarget(r.surface)
                        req.set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_PICTURE)
                        s.setRepeatingRequest(req.build(), null, bg)
                    }

                    override fun onConfigureFailed(s: CameraCaptureSession) = fail()
                }, bg)
            }

            override fun onDisconnected(cam: CameraDevice) = cam.close()

            override fun onError(cam: CameraDevice, error: Int) {
                cam.close()
                fail()
            }
        }, bg)
    }

    // Landscape frames shown rotated in a portrait view: scale both axes alike and crop, not stretch.
    private fun fitPreview(size: Size) {
        val vw = texture.width.toFloat()
        val vh = texture.height.toFloat()
        if (vw == 0f || vh == 0f) return
        val cw = size.height.toFloat()
        val ch = size.width.toFloat()
        val scale = maxOf(vw / cw, vh / ch)
        val m = Matrix()
        m.setScale(cw * scale / vw, ch * scale / vh, vw / 2, vh / 2)
        runOnUiThread { texture.setTransform(m) }
    }

    private fun decode(r: ImageReader) {
        val img = r.acquireLatestImage() ?: return
        try {
            if (done) return
            val plane = img.planes[0]
            val buf = plane.buffer
            val data = ByteArray(plane.rowStride * img.height)
            buf.get(data, 0, minOf(buf.remaining(), data.size))
            val src = PlanarYUVLuminanceSource(data, plane.rowStride, img.height, 0, 0, img.width, img.height, false)
            val text = qr.decode(BinaryBitmap(HybridBinarizer(src)), hints).text
            done = true
            runOnUiThread {
                setResult(RESULT_OK, Intent().putExtra(EXTRA_TEXT, text))
                finish()
            }
        } catch (_: ReaderException) {
            // No code in this frame; the next one comes in a few ms.
        } finally {
            qr.reset()
            img.close()
        }
    }

    private fun fail() {
        runOnUiThread {
            setResult(RESULT_FAILED)
            finish()
        }
    }

    companion object {
        const val EXTRA_TEXT = "dev.scorix.scan.text"
        const val RESULT_DENIED = RESULT_FIRST_USER
        const val RESULT_FAILED = RESULT_FIRST_USER + 1
    }
}

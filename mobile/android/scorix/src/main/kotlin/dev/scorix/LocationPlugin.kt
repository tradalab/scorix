package dev.scorix

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock

@SuppressLint("MissingPermission")
internal class LocationPlugin : LocationPluginBase() {
    override fun current(req: LocationRequest, reply: Reply<Position>) {
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "the app is not started")
        val wanted = if (req.coarse) listOf(COARSE) else listOf(FINE, COARSE)
        val declared = c.packageManager.getPackageInfo(c.packageName, PackageManager.GET_PERMISSIONS)
            .requestedPermissions?.toSet().orEmpty()
        val ask = wanted.filter { it in declared }
        if (ask.isEmpty()) {
            return reply.reject(
                "unavailable",
                "the app does not declare location: add <uses-permission android:name=\"android.permission.ACCESS_FINE_LOCATION\" /> to its AndroidManifest.xml",
            )
        }
        val guarded = { try { locate(c, req, reply) } catch (e: RuntimeException) { reply.reject("failed", e.message ?: e.toString()) } }
        val any = ask.any { granted(c, it) }
        val enough = if (req.coarse || FINE !in ask) any else granted(c, FINE) || (any && askedPrecise)
        if (enough) return guarded()
        if (any) askedPrecise = true
        reply.call.requestPermissions(ask.toTypedArray()) {
            if (ask.any { granted(c, it) }) guarded() else reply.reject("denied", "the user did not allow location")
        }
    }

    private fun locate(c: Context, req: LocationRequest, reply: Reply<Position>) {
        val lm = c.getSystemService(LocationManager::class.java)
        val fine = !req.coarse && granted(c, FINE)
        val fused = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) listOf(LocationManager.FUSED_PROVIDER) else emptyList()
        val providers = (fused + if (fine) listOf(LocationManager.GPS_PROVIDER, LocationManager.NETWORK_PROVIDER) else listOf(LocationManager.NETWORK_PROVIDER))
            .filter { lm.allProviders.contains(it) && lm.isProviderEnabled(it) }
        if (providers.isEmpty()) return reply.reject("disabled", "location is turned off on this device")

        if (req.maxAgeMs > 0) {
            val now = System.currentTimeMillis()
            val cached = providers.mapNotNull { lm.getLastKnownLocation(it) }
                .filter { now - it.time in 0..req.maxAgeMs && (!fine || accuracyOf(it) <= CACHED_ACCURACY_M) }
                .minByOrNull { accuracyOf(it) }
            if (cached != null) return reply.resolve(cached.toPosition())
        }

        val main = Handler(Looper.getMainLooper())
        var best: Location? = null
        var done = false
        lateinit var listener: LocationListener
        val finish = finish@{ fix: Location? ->
            if (done) return@finish
            done = true
            main.removeCallbacksAndMessages(listener)
            lm.removeUpdates(listener)
            if (fix == null) reply.reject("timeout", "no position fix within ${timeoutOf(req)} ms") else reply.resolve(fix.toPosition())
        }
        listener = object : LocationListener {
            override fun onLocationChanged(location: Location) {
                if (best == null || accuracyOf(location) < accuracyOf(best!!)) best = location
                if (!fine || accuracyOf(location) <= GOOD_ACCURACY_M) finish(location)
            }

            @Deprecated("Deprecated in Java")
            override fun onStatusChanged(provider: String?, status: Int, extras: Bundle?) {}

            override fun onProviderEnabled(provider: String) {}

            override fun onProviderDisabled(provider: String) {
                if (providers.none { lm.isProviderEnabled(it) }) finish(best)
            }
        }
        for (p in providers) lm.requestLocationUpdates(p, 0L, 0f, listener, Looper.getMainLooper())
        main.postAtTime({ best?.takeIf { accuracyOf(it) <= SETTLE_ACCURACY_M }?.let { finish(it) } }, listener, SystemClock.uptimeMillis() + SETTLE_MS)
        main.postAtTime({ finish(best) }, listener, SystemClock.uptimeMillis() + timeoutOf(req))
    }

    private fun Location.toPosition() = Position(
        latitude = latitude,
        longitude = longitude,
        accuracyM = if (hasAccuracy()) accuracy.toDouble() else 0.0,
        altitudeM = if (hasAltitude()) altitude else 0.0,
        speedMps = if (hasSpeed()) speed.toDouble() else 0.0,
        bearingDeg = if (hasBearing()) bearing.toDouble() else 0.0,
        hasMotion = hasSpeed() || hasBearing(),
        timeMs = time,
        provider = provider ?: "",
    )

    private fun accuracyOf(l: Location) = if (l.hasAccuracy()) l.accuracy else Float.MAX_VALUE

    private fun timeoutOf(req: LocationRequest) = if (req.timeoutMs > 0) req.timeoutMs else DEFAULT_TIMEOUT_MS

    private fun granted(c: Context, permission: String) = c.checkSelfPermission(permission) == PackageManager.PERMISSION_GRANTED

    private companion object {
        @Volatile
        var askedPrecise = false
        const val FINE = Manifest.permission.ACCESS_FINE_LOCATION
        const val COARSE = Manifest.permission.ACCESS_COARSE_LOCATION
        const val DEFAULT_TIMEOUT_MS = 30_000L
        const val GOOD_ACCURACY_M = 25f
        const val SETTLE_ACCURACY_M = 100f
        const val CACHED_ACCURACY_M = 100f
        const val SETTLE_MS = 10_000L
    }
}

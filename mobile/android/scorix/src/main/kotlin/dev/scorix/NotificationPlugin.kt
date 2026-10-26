package dev.scorix

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.drawable.Icon
import android.net.Uri
import android.os.Build

internal class NotificationPlugin : NotificationPluginBase() {
    // From the clock, not 0: a new process would otherwise replace what the last one left showing.
    private var next = (System.currentTimeMillis() / 1000).toInt()

    override fun show(req: NotificationContent, reply: Reply<Empty>) {
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "the app is not started")
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            c.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
        ) {
            return post(c, req, reply)
        }
        val declared = c.packageManager.getPackageInfo(c.packageName, PackageManager.GET_PERMISSIONS)
            .requestedPermissions?.contains(Manifest.permission.POST_NOTIFICATIONS) == true
        if (!declared) {
            return reply.reject(
                "unavailable",
                "the app does not declare notifications: add <uses-permission android:name=\"android.permission.POST_NOTIFICATIONS\" /> to its AndroidManifest.xml",
            )
        }
        reply.call.requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS)) { granted ->
            if (granted) post(c, req, reply) else reply.reject("denied", "the user did not allow notifications")
        }
    }

    // Also run from the permission callback, outside the host's catch, where a throw kills the app.
    private fun post(c: Context, req: NotificationContent, reply: Reply<Empty>) = try {
        notify(c, req, reply)
    } catch (e: RuntimeException) {
        reply.reject("failed", e.message ?: e.toString())
    }

    private fun notify(c: Context, req: NotificationContent, reply: Reply<Empty>) {
        val nm = c.getSystemService(NotificationManager::class.java)
        // Below Android 13 there is no prompt, only the switch in Settings.
        if (!nm.areNotificationsEnabled()) return reply.reject("denied", "notifications are off for this app")
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(NotificationChannel(CHANNEL, c.applicationInfo.loadLabel(c.packageManager), NotificationManager.IMPORTANCE_DEFAULT))
            Notification.Builder(c, CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(c)
        }
        // Android refuses a notification without one, and an app need not have an icon.
        b.setSmallIcon(c.applicationInfo.icon.takeIf { it != 0 } ?: android.R.drawable.ic_dialog_info)
            .setContentTitle(req.title)
            .setContentText(req.text)
            .setStyle(Notification.BigTextStyle().bigText(req.text))
            .setAutoCancel(true)
            .setContentIntent(open(c, req.url))
        for (btn in req.buttons) b.addAction(Notification.Action.Builder(null as Icon?, btn.label, open(c, btn.url)).build())
        // A keyed one replaces itself by tag; keyless ones each take a new id.
        if (req.key.isEmpty()) nm.notify(next++, b.build()) else nm.notify(req.key, 0, b.build())
        reply.resolve(Empty())
    }

    // The launcher named explicitly, so a click needs no intent filter; Go reads the url as a deep link.
    private fun open(c: Context, url: String): PendingIntent {
        val launch = c.packageManager.getLaunchIntentForPackage(c.packageName) ?: Intent()
        val intent = if (url.isEmpty()) launch else Intent(Intent.ACTION_VIEW, Uri.parse(url)).setComponent(launch.component)
        return PendingIntent.getActivity(c, url.hashCode(), intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private companion object {
        const val CHANNEL = "scorix"
    }
}

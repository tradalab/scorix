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
    override fun show(req: NotificationContent, reply: Reply<Empty>) = withPermission(reply) { c ->
        // Below Android 13 there is no prompt, only the switch in Settings.
        if (!Notifications.post(c, req)) reply.reject("denied", "notifications are off for this app")
        else reply.resolve(Empty())
    }

    override fun schedule(req: NotificationSchedule, reply: Reply<Empty>) {
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "the app is not started")
        if (!NotificationAlarm.declared(c)) {
            return reply.reject("unavailable", "the app's manifest lacks dev.scorix.NotificationAlarm: run scorix mobile sync with the notification module on")
        }
        // Clearing must not prompt: an app turning its reminders off would otherwise ask to post.
        if (req.items.isEmpty()) {
            NotificationAlarm.replace(c, emptyList())
            return reply.resolve(Empty())
        }
        withPermission(reply) {
            // Below Android 13 the switch in Settings is the only refusal, and nothing would ever show.
            if (!it.getSystemService(NotificationManager::class.java).areNotificationsEnabled()) {
                return@withPermission reply.reject("denied", "notifications are off for this app")
            }
            NotificationAlarm.replace(it, req.items)
            reply.resolve(Empty())
        }
    }

    private fun withPermission(reply: Reply<Empty>, then: (Context) -> Unit) {
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "the app is not started")
        // Also run from the permission callback, outside the host's catch, where a throw kills the app.
        val guarded = { try { then(c) } catch (e: RuntimeException) { reply.reject("failed", e.message ?: e.toString()) } }
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            c.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
        ) {
            return guarded()
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
            if (granted) guarded() else reply.reject("denied", "the user did not allow notifications")
        }
    }
}

// Shared with NotificationAlarm, which posts from a process that may have no Activity.
internal object Notifications {
    private const val CHANNEL = "scorix"

    // From the clock, not 0: a new process would otherwise replace what the last one left showing.
    private var next = (System.currentTimeMillis() / 1000).toInt()

    fun post(c: Context, req: NotificationContent): Boolean {
        val nm = c.getSystemService(NotificationManager::class.java)
        if (!nm.areNotificationsEnabled()) return false
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
        return true
    }

    // The launcher named explicitly, so a click needs no intent filter; Go reads the url as a deep link.
    fun open(c: Context, url: String): PendingIntent {
        val launch = c.packageManager.getLaunchIntentForPackage(c.packageName) ?: Intent()
        val intent = if (url.isEmpty()) launch else Intent(Intent.ACTION_VIEW, Uri.parse(url)).setComponent(launch.component)
        return PendingIntent.getActivity(c, url.hashCode(), intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }
}

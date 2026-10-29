package dev.scorix

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import org.json.JSONArray
import org.json.JSONException

// Declared by the manifest scorix mobile sync writes, not the library's: the boot receiver and
// its permission belong only to apps that schedule.
class NotificationAlarm : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        // Reboot, update and a clock change all drop or skew the alarm; firing on any of them is
        // harmless since only what is due gets posted.
        fire(context)
    }

    internal companion object {
        private const val PREFS = "scorix.notification"
        private const val KEY = "schedule"

        // A phone off at the hour still gets the morning's reminder at boot, not yesterday's.
        private const val LATE_MS = 12 * 60 * 60 * 1000L

        fun declared(c: Context) = try {
            c.packageManager.getReceiverInfo(ComponentName(c, NotificationAlarm::class.java), 0)
            true
        } catch (_: PackageManager.NameNotFoundException) {
            false
        }

        // Synchronized with fire, which would otherwise save the old list over the new one.
        @Synchronized
        fun replace(c: Context, items: List<ScheduledNotification>) {
            // An app opened while the inexact alarm lags passes that time as past, dropping it unshown.
            // A clear is reminders turned off, so nothing more goes out.
            if (items.isNotEmpty()) postDue(c, load(c))
            val now = System.currentTimeMillis()
            val rest = items.filter { it.atMs > now }
            save(c, rest)
            arm(c, rest)
        }

        @Synchronized
        private fun fire(c: Context) {
            val rest = postDue(c, load(c))
            save(c, rest)
            arm(c, rest)
        }

        private fun postDue(c: Context, items: List<ScheduledNotification>): List<ScheduledNotification> {
            val now = System.currentTimeMillis()
            val (due, rest) = items.partition { it.atMs <= now }
            for (n in due) {
                if (now - n.atMs > LATE_MS) continue
                // A permission revoked since scheduling turns the post into a throw; the rest still go.
                try { Notifications.post(c, n.content) } catch (_: RuntimeException) {}
            }
            return rest
        }

        // One alarm for the earliest, re-armed on each fire: an app may hold only about 500.
        private fun arm(c: Context, items: List<ScheduledNotification>) {
            val am = c.getSystemService(AlarmManager::class.java)
            val pi = PendingIntent.getBroadcast(
                c, 0, Intent(c, NotificationAlarm::class.java),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            val at = items.minOfOrNull { it.atMs } ?: return am.cancel(pi)
            // Inexact on purpose: an exact alarm needs a permission Play reviews per app.
            am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, at, pi)
        }

        private fun load(c: Context): List<ScheduledNotification> {
            val raw = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(KEY, null) ?: return emptyList()
            // A throw here would recur at every boot; a lost schedule comes back on the app's next start.
            return try {
                val a = JSONArray(raw)
                List(a.length()) { ScheduledNotification.fromJson(a.getJSONObject(it)) }
            } catch (_: JSONException) {
                emptyList()
            }
        }

        private fun save(c: Context, items: List<ScheduledNotification>) {
            val a = JSONArray().apply { items.forEach { put(it.toJson()) } }
            c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(KEY, a.toString()).commit()
        }
    }
}

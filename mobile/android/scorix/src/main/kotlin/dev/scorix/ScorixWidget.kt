package dev.scorix

import android.app.AlarmManager
import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.appwidget.AppWidgetProviderInfo
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.os.Build
import android.util.SizeF
import android.view.View
import android.widget.RemoteViews
import org.json.JSONException
import org.json.JSONObject

/**
 * Subclassed per android.widgets entry by scorix mobile sync. Draws from the stored timeline alone:
 * Go is not loaded in this process.
 */
open class ScorixWidget : AppWidgetProvider() {
    override fun onReceive(context: Context, intent: Intent) {
        val self = ComponentName(context, javaClass)
        val id = intent.getIntExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, AppWidgetManager.INVALID_APPWIDGET_ID)
        when (intent.action) {
            STEP -> step(context, self, id, intent.getIntExtra(DELTA, 0))
            AppWidgetManager.ACTION_APPWIDGET_DELETED -> forget(context, self, id)
        }
        refresh(context, self)
    }

    internal companion object {
        private const val KEY = "timeline"
        private const val KIND = "dev.scorix.widget.kind"
        private const val LAYOUT = "dev.scorix.widget.layout"
        private const val DAY_MS = 24 * 60 * 60 * 1000L

        private const val TICK = "dev.scorix.widget.TICK"
        private const val STEP = "dev.scorix.widget.STEP"
        private const val DELTA = "dev.scorix.widget.DELTA"
        private const val WINDOW_MS = 10 * 60_000L

        private class Card(val w: Float, val h: Float, val layout: Int, val text: Boolean = true, val rows: Boolean = false)

        private val SIZES = mapOf(
            "card" to listOf(
                Card(0f, 0f, R.layout.scorix_widget_card_small, text = false),
                Card(100f, 100f, R.layout.scorix_widget_card),
                Card(250f, 40f, R.layout.scorix_widget_card_wide),
                Card(250f, 150f, R.layout.scorix_widget_card_large, rows = true),
            ),
            "clock" to listOf(
                Card(0f, 0f, R.layout.scorix_widget_clock_small, text = false),
                Card(100f, 100f, R.layout.scorix_widget_clock),
                Card(250f, 40f, R.layout.scorix_widget_clock_wide),
                Card(250f, 150f, R.layout.scorix_widget_clock_large, rows = true),
            ),
        )

        fun component(c: Context, kind: String): ComponentName? {
            @Suppress("DEPRECATION")
            val receivers = c.packageManager.getPackageInfo(c.packageName, PackageManager.GET_RECEIVERS or PackageManager.GET_META_DATA).receivers
            val r = receivers?.firstOrNull { it.metaData?.getString(KIND) == kind } ?: return null
            return ComponentName(c, r.name)
        }

        @Synchronized
        fun replace(c: Context, self: ComponentName, t: WidgetTimeline) {
            prefs(c, t.kind).edit().putString(KEY, t.toJson().toString()).commit()
            refresh(c, self, preview = true)
        }

        @Synchronized
        fun refresh(c: Context, self: ComponentName, preview: Boolean = false) {
            val meta = c.packageManager.getReceiverInfo(self, PackageManager.GET_META_DATA).metaData
            val kind = meta?.getString(KIND) ?: return
            val t = load(c, kind)
            val now = System.currentTimeMillis()
            val sorted = t.entries.sortedBy { it.atMs }
            val due = due(t, sorted, now)
            val entry = if (due < 0) t.fallback else sorted[due]

            val wm = AppWidgetManager.getInstance(c)
            val open = Notifications.open(c, t.url)
            val sizes = SIZES[meta.getString(LAYOUT)]
            if (preview && Build.VERSION.SDK_INT >= Build.VERSION_CODES.VANILLA_ICE_CREAM) {
                val views = if (sizes == null) grid(c, self, 0, sorted, due, due, entry, now, open) else sized(c, sizes, entry, open)
                wm.setWidgetPreview(self, AppWidgetProviderInfo.WIDGET_CATEGORY_HOME_SCREEN, views)
            }
            val ids = wm.getAppWidgetIds(self)
            if (ids.isEmpty()) return arm(c, self, null)
            val drawn = mutableListOf(entry)
            if (sizes == null) {
                for (id in ids) {
                    val shown = if (due < 0) -1 else due + offset(prefs(c, kind), id, sorted[due].atMs).coerceIn(-due, sorted.size - 1 - due)
                    val e = if (shown < 0) entry else sorted[shown]
                    drawn += e
                    wm.updateAppWidget(id, grid(c, self, id, sorted, due, shown, e, now, open))
                }
            } else if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                wm.updateAppWidget(self, sized(c, sizes, entry, open))
            } else {
                for (id in ids) {
                    val o = wm.getAppWidgetOptions(id)
                    val size = pick(sizes, o.getInt(AppWidgetManager.OPTION_APPWIDGET_MIN_WIDTH).toFloat(), o.getInt(AppWidgetManager.OPTION_APPWIDGET_MAX_HEIGHT).toFloat())
                    wm.updateAppWidget(id, card(c, size, entry, open))
                }
            }
            val ends = drawn.flatMap { e -> e.cells.filter { it.atMs > 0 }.map { it.atMs + DAY_MS } }
            val nextAt = (t.entries.map { it.atMs } + t.expiresMs + ends)
                .filter { it > now }.minOrNull()
            arm(c, self, nextAt)
        }

        @Synchronized
        private fun step(c: Context, self: ComponentName, id: Int, delta: Int) {
            val kind = c.packageManager.getReceiverInfo(self, PackageManager.GET_META_DATA).metaData?.getString(KIND) ?: return
            val t = load(c, kind)
            val sorted = t.entries.sortedBy { it.atMs }
            val due = due(t, sorted, System.currentTimeMillis())
            if (due < 0) return
            val p = prefs(c, kind)
            val n = if (delta == 0) 0 else (offset(p, id, sorted[due].atMs) + delta).coerceIn(-due, sorted.size - 1 - due)
            if (n == 0) p.edit().remove("step.$id").commit() else p.edit().putString("step.$id", "${sorted[due].atMs}:$n").commit()
        }

        @Synchronized
        private fun forget(c: Context, self: ComponentName, id: Int) {
            val kind = c.packageManager.getReceiverInfo(self, PackageManager.GET_META_DATA).metaData?.getString(KIND) ?: return
            prefs(c, kind).edit().remove("step.$id").commit()
        }

        private fun load(c: Context, kind: String) = prefs(c, kind).getString(KEY, null)
            ?.let { try { WidgetTimeline.fromJson(JSONObject(it)) } catch (_: JSONException) { null } } ?: WidgetTimeline()

        private fun due(t: WidgetTimeline, sorted: List<WidgetEntry>, now: Long) =
            if (t.expiresMs != 0L && now >= t.expiresMs) -1 else sorted.indexOfLast { it.atMs <= now }

        private fun offset(p: SharedPreferences, id: Int, dueAt: Long): Int {
            val parts = p.getString("step.$id", null)?.split(':') ?: return 0
            return if (parts.size == 2 && parts[0].toLongOrNull() == dueAt) parts[1].toIntOrNull() ?: 0 else 0
        }

        private fun prefs(c: Context, kind: String) = c.getSharedPreferences("scorix.widget.$kind", Context.MODE_PRIVATE)

        private fun pick(sizes: List<Card>, w: Float, h: Float) = sizes.filter { it.w <= w && it.h <= h }
            .minByOrNull { (it.w - w) * (it.w - w) + (it.h - h) * (it.h - h) } ?: sizes[0]

        @androidx.annotation.RequiresApi(Build.VERSION_CODES.S)
        private fun sized(c: Context, sizes: List<Card>, e: WidgetEntry, open: PendingIntent) =
            RemoteViews(sizes.associate { SizeF(it.w, it.h) to card(c, it, e, open) })

        private fun card(c: Context, size: Card, e: WidgetEntry, open: PendingIntent): RemoteViews {
            val v = RemoteViews(c.packageName, size.layout)
            show(v, R.id.scorix_widget_caption, e.caption)
            titles(c, v, e)
            show(v, R.id.scorix_widget_text, if (size.text) e.text else "")
            if (size.rows) {
                v.removeAllViews(R.id.scorix_widget_rows)
                for (r in e.rows) {
                    val row = RemoteViews(c.packageName, if (r.accent) R.layout.scorix_widget_row_accent else R.layout.scorix_widget_row)
                    row.setTextViewText(R.id.scorix_widget_row_label, r.label)
                    row.setTextViewText(R.id.scorix_widget_row_value, r.value)
                    v.addView(R.id.scorix_widget_rows, row)
                }
            }
            v.setOnClickPendingIntent(R.id.scorix_widget_root, open)
            return v
        }

        private fun grid(c: Context, self: ComponentName, id: Int, sorted: List<WidgetEntry>, due: Int, shown: Int, e: WidgetEntry, now: Long, open: PendingIntent): RemoteViews {
            val v = RemoteViews(c.packageName, R.layout.scorix_widget_grid)
            titles(c, v, e)
            show(v, R.id.scorix_widget_text, e.text)
            val steps = listOf(R.id.scorix_widget_prev to (shown > 0), R.id.scorix_widget_next to (shown in 0 until sorted.size - 1))
            for ((view, on) in steps) v.setViewVisibility(view, if (on) View.VISIBLE else View.INVISIBLE)
            v.setOnClickPendingIntent(R.id.scorix_widget_prev, stepIntent(c, self, id, -1))
            v.setOnClickPendingIntent(R.id.scorix_widget_next, stepIntent(c, self, id, 1))
            val away = shown != due
            v.setViewVisibility(R.id.scorix_widget_back, if (away) View.VISIBLE else View.GONE)
            if (away) {
                v.setOnClickPendingIntent(R.id.scorix_widget_back, stepIntent(c, self, id, 0))
                v.setOnClickPendingIntent(R.id.scorix_widget_head, stepIntent(c, self, id, 0))
            }
            v.removeAllViews(R.id.scorix_widget_cells)
            for (week in e.cells.chunked(7)) {
                val header = week.all { it.atMs == 0L }
                val row = RemoteViews(c.packageName, if (header) R.layout.scorix_widget_grid_header else R.layout.scorix_widget_grid_row)
                for (cell in week + List(7 - week.size) { WidgetCell() }) row.addView(R.id.scorix_widget_grid_row, cellView(c, cell, now))
                v.addView(R.id.scorix_widget_cells, row)
            }
            v.setOnClickPendingIntent(R.id.scorix_widget_root, open)
            return v
        }

        private fun cellView(c: Context, cell: WidgetCell, now: Long): RemoteViews {
            val layout = when {
                cell.atMs > 0 && now >= cell.atMs && now < cell.atMs + DAY_MS -> R.layout.scorix_widget_cell_today
                cell.muted -> R.layout.scorix_widget_cell_muted
                cell.accent -> R.layout.scorix_widget_cell_accent
                else -> R.layout.scorix_widget_cell
            }
            val v = RemoteViews(c.packageName, layout)
            v.setTextViewText(R.id.scorix_widget_cell_text, cell.text)
            show(v, R.id.scorix_widget_cell_sub, cell.sub)
            if (cell.url.isNotEmpty()) v.setOnClickPendingIntent(R.id.scorix_widget_cell, Notifications.open(c, cell.url))
            return v
        }

        private fun titles(c: Context, v: RemoteViews, e: WidgetEntry) {
            val title = e.title.ifEmpty { c.applicationInfo.loadLabel(c.packageManager).toString() }
            show(v, R.id.scorix_widget_title, if (e.accent) "" else title)
            show(v, R.id.scorix_widget_title_accent, if (e.accent) title else "")
        }

        private fun show(v: RemoteViews, id: Int, text: String) {
            v.setTextViewText(id, text)
            v.setViewVisibility(id, if (text.isEmpty()) View.GONE else View.VISIBLE)
        }

        private fun arm(c: Context, self: ComponentName, at: Long?) {
            val am = c.getSystemService(AlarmManager::class.java)
            val pi = PendingIntent.getBroadcast(
                c, 0, Intent(TICK).setComponent(self),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            if (at == null) am.cancel(pi) else am.setWindow(AlarmManager.RTC, at, WINDOW_MS, pi)
        }

        private fun stepIntent(c: Context, self: ComponentName, id: Int, delta: Int) = PendingIntent.getBroadcast(
            c, id * 3 + delta + 1,
            Intent(STEP).setComponent(self).putExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, id).putExtra(DELTA, delta),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }
}

internal class WidgetPlugin : WidgetPluginBase() {
    override fun setTimeline(req: WidgetTimeline, reply: Reply<Empty>) {
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "the app is not started")
        val self = ScorixWidget.component(c, req.kind) ?: return reply.reject(
            "unavailable",
            "the app's manifest has no widget of kind \"${req.kind}\": declare it under android.widgets in scorix.yaml and run scorix mobile sync",
        )
        ScorixWidget.replace(c, self, req)
        reply.resolve(Empty())
    }

    override fun requestPin(req: WidgetKind, reply: Reply<Empty>) {
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "the app is not started")
        val self = ScorixWidget.component(c, req.kind) ?: return reply.reject(
            "unavailable",
            "the app's manifest has no widget of kind \"${req.kind}\": declare it under android.widgets in scorix.yaml and run scorix mobile sync",
        )
        val wm = AppWidgetManager.getInstance(c)
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O || !wm.isRequestPinAppWidgetSupported || !wm.requestPinAppWidget(self, null, null)) {
            return reply.reject("unavailable", "the launcher cannot place widgets for an app: add it from the launcher's widget list")
        }
        reply.resolve(Empty())
    }
}

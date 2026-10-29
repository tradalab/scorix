package dev.scorix

import android.content.ContentProvider
import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import java.io.File

/**
 * Serves the file SharePlugin just wrote, read-only. Not androidx FileProvider: androidx core comes
 * only through webkit, and one flat directory needs none of its path mapping.
 */
class ShareProvider : ContentProvider() {
    override fun onCreate() = true

    private fun file(uri: Uri): File {
        val ctx = context ?: throw IllegalStateException("no context")
        val name = uri.lastPathSegment ?: throw SecurityException("no file name")
        val f = File(dir(ctx), name)
        // Only a direct child: a decoded "../" segment must not reach the rest of the cache.
        if (f.parentFile?.canonicalPath != dir(ctx).canonicalPath || !f.isFile) throw SecurityException("not shared: $name")
        return f
    }

    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor {
        if (mode != "r") throw SecurityException("shared files are read-only")
        return ParcelFileDescriptor.open(file(uri), ParcelFileDescriptor.MODE_READ_ONLY)
    }

    override fun getType(uri: Uri): String =
        MimeTypeMap.getSingleton().getMimeTypeFromExtension(uri.lastPathSegment?.substringAfterLast('.', "")) ?: "application/octet-stream"

    // Messaging apps ask for the name and size before they read, and drop the attachment without them.
    override fun query(uri: Uri, projection: Array<String>?, selection: String?, args: Array<String>?, sort: String?): Cursor {
        val f = file(uri)
        val cols = projection ?: arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE)
        return MatrixCursor(cols, 1).apply {
            addRow(cols.map { if (it == OpenableColumns.SIZE) f.length() else if (it == OpenableColumns.DISPLAY_NAME) f.name else null })
        }
    }

    override fun insert(uri: Uri, values: ContentValues?): Uri? = null

    override fun delete(uri: Uri, selection: String?, args: Array<String>?) = 0

    override fun update(uri: Uri, values: ContentValues?, selection: String?, args: Array<String>?) = 0

    companion object {
        fun dir(ctx: Context) = File(ctx.cacheDir, "scorix-share")

        fun uri(ctx: Context, name: String): Uri =
            Uri.Builder().scheme("content").authority("${ctx.packageName}.scorix.share").appendPath(name).build()
    }
}

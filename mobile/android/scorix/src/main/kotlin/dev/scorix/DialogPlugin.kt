package dev.scorix

import android.app.Activity
import android.app.AlertDialog
import android.content.ContentResolver
import android.content.Intent
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.provider.DocumentsContract
import android.provider.OpenableColumns
import org.json.JSONObject
import java.io.FileNotFoundException

/**
 * The picker returns a content URI, not a path, so file bytes travel through
 * the call: fine for documents an app imports and exports, not media libraries.
 */
internal class DialogPlugin : ScorixPlugin {
    override val name = "dialog"

    override fun call(method: String, args: JSONObject, call: PluginCall) {
        when (method) {
            "openDocument" -> {
                val intent = Intent(Intent.ACTION_OPEN_DOCUMENT)
                    .addCategory(Intent.CATEGORY_OPENABLE)
                    .setType(args.optString("mime").ifEmpty { "*/*" })
                call.startForResult(intent) { code, data ->
                    val uri = data?.data
                    val a = call.activity
                    if (code != Activity.RESULT_OK || uri == null || a == null) return@startForResult call.reject("canceled", "no file picked")
                    val bytes = a.contentResolver.openInputStream(uri)?.use { it.readBytes() }
                        ?: return@startForResult call.reject("failed", "cannot read $uri")
                    call.resolve(JSONObject().put("name", displayName(a, uri)).put("data", encode(bytes)))
                }
            }
            "createDocument" -> {
                val intent = Intent(Intent.ACTION_CREATE_DOCUMENT)
                    .addCategory(Intent.CATEGORY_OPENABLE)
                    .setType(args.optString("mime").ifEmpty { "application/octet-stream" })
                    .putExtra(Intent.EXTRA_TITLE, args.optString("fileName"))
                val bytes = decode(args.optString("data"))
                call.startForResult(intent) { code, data ->
                    val uri = data?.data
                    val a = call.activity
                    if (code != Activity.RESULT_OK || uri == null || a == null) return@startForResult call.reject("canceled", "no file chosen")
                    val out = a.contentResolver.openOutputStream(uri, "wt")
                        ?: return@startForResult call.reject("failed", "cannot write $uri")
                    out.use { it.write(bytes) }
                    call.resolve(JSONObject().put("name", displayName(a, uri)))
                }
            }
            "openTree" -> {
                call.startForResult(Intent(Intent.ACTION_OPEN_DOCUMENT_TREE)) { code, data ->
                    val tree = data?.data
                    val a = call.activity
                    if (code != Activity.RESULT_OK || tree == null || a == null) return@startForResult call.reject("canceled", "no folder picked")
                    // Persisted, or the grant ends with this process and a write after restart is refused.
                    a.contentResolver.takePersistableUriPermission(tree, Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
                    val dir = DocumentsContract.buildDocumentUriUsingTree(tree, DocumentsContract.getTreeDocumentId(tree))
                    call.resolve(JSONObject().put("id", tree.toString()).put("name", displayName(a, dir)))
                }
            }
            "writeTree" -> {
                val c = ScorixHost.appContext ?: return call.reject("unavailable", "the app is not started")
                val tree = Uri.parse(args.getString("tree"))
                val name = args.getString("name")
                val mime = args.optString("mime").ifEmpty { "application/octet-stream" }
                val bytes = decode(args.optString("data"))
                // Off the main thread: a cloud provider may answer over the network.
                Thread {
                    val result = runCatching { writeTree(c.contentResolver, tree, name, mime, bytes) }
                    main.post {
                        val e = result.exceptionOrNull()
                        when (e) {
                            null -> call.resolve()
                            is SecurityException, is FileNotFoundException, is IllegalArgumentException -> call.reject("folder_gone", e.message ?: "folder not reachable")
                            else -> call.reject("failed", e.message ?: e.toString())
                        }
                    }
                }.start()
            }
            "pickFile" -> {
                val intent = Intent(Intent.ACTION_CREATE_DOCUMENT)
                    .addCategory(Intent.CATEGORY_OPENABLE)
                    .setType(args.optString("mime").ifEmpty { "application/octet-stream" })
                    .putExtra(Intent.EXTRA_TITLE, args.optString("fileName"))
                call.startForResult(intent) { code, data ->
                    val uri = data?.data
                    if (code != Activity.RESULT_OK || uri == null) return@startForResult call.reject("canceled", "no file chosen")
                    val cr = (call.activity ?: ScorixHost.appContext)?.contentResolver
                        ?: return@startForResult call.reject("unavailable", "the app is not started")
                    // Persisted, or the grant ends with this process. Refusing here beats failing at the
                    // first write after a restart, and the empty file the save screen made is taken back.
                    val granted = data.flags and (Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
                    try {
                        if (granted and Intent.FLAG_GRANT_WRITE_URI_PERMISSION == 0) throw SecurityException("no write grant for $uri")
                        cr.takePersistableUriPermission(uri, granted)
                    } catch (e: SecurityException) {
                        runCatching { DocumentsContract.deleteDocument(cr, uri) }
                        return@startForResult call.reject("not_persistable", e.message ?: "no lasting access to $uri")
                    }
                    val a = call.activity
                    call.resolve(JSONObject().put("id", uri.toString()).put("name", if (a != null) displayName(a, uri) else uri.lastPathSegment ?: ""))
                }
            }
            "rewriteFile" -> {
                val c = ScorixHost.appContext ?: return call.reject("unavailable", "the app is not started")
                val uri = Uri.parse(args.getString("uri"))
                val bytes = decode(args.optString("data"))
                // Off the main thread: a cloud provider may answer over the network.
                Thread {
                    val result = runCatching { rewrite(c.contentResolver, uri, bytes) }
                    main.post {
                        when (val e = result.exceptionOrNull()) {
                            null -> call.resolve()
                            is SecurityException, is FileNotFoundException -> call.reject("folder_gone", e.message ?: "file not reachable")
                            else -> call.reject("failed", e.message ?: e.toString())
                        }
                    }
                }.start()
            }
            "message" -> {
                val a = call.activity ?: return call.reject("no_activity", "a message needs an Activity on screen")
                val d = AlertDialog.Builder(a)
                    .setTitle(args.optString("title"))
                    .setMessage(args.optString("text"))
                    .setPositiveButton(android.R.string.ok, null)
                    .setOnDismissListener { call.resolve() }
                    .show()
                call.onCancel { d.dismiss() }
            }
            else -> call.reject("no_method", "dialog has no method $method")
        }
    }

    private val main = Handler(Looper.getMainLooper())

    // Replaces in place so the folder keeps one copy, not the "name (1)" createDocument would make.
    private fun writeTree(cr: ContentResolver, tree: Uri, name: String, mime: String, bytes: ByteArray) {
        if (cr.persistedUriPermissions.none { it.uri == tree && it.isWritePermission }) {
            throw SecurityException("no write grant for $tree")
        }
        val parent = DocumentsContract.getTreeDocumentId(tree)
        val children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, parent)
        val cols = arrayOf(DocumentsContract.Document.COLUMN_DOCUMENT_ID, DocumentsContract.Document.COLUMN_DISPLAY_NAME)
        val existing = cr.query(children, cols, null, null, null)?.use { cur ->
            var id: String? = null
            while (id == null && cur.moveToNext()) if (cur.getString(1) == name) id = cur.getString(0)
            id
        }
        val doc = existing?.let { DocumentsContract.buildDocumentUriUsingTree(tree, it) }
            ?: DocumentsContract.createDocument(cr, DocumentsContract.buildDocumentUriUsingTree(tree, parent), mime, name)
            ?: throw FileNotFoundException("cannot create $name")
        (cr.openOutputStream(doc, "wt") ?: throw FileNotFoundException("cannot write $name")).use { it.write(bytes) }
    }

    private fun rewrite(cr: ContentResolver, uri: Uri, bytes: ByteArray) {
        if (cr.persistedUriPermissions.none { it.uri == uri && it.isWritePermission }) {
            throw SecurityException("no write grant for $uri")
        }
        // A provider refusing "wt" gets plain "w", which may leave an old tail behind;
        // readers of such files must stop at the end of their own content.
        val out = try {
            cr.openOutputStream(uri, "wt")
        } catch (e: IllegalArgumentException) {
            cr.openOutputStream(uri, "w")
        } catch (e: UnsupportedOperationException) {
            cr.openOutputStream(uri, "w")
        }
        (out ?: throw FileNotFoundException("cannot write $uri")).use { it.write(bytes) }
    }

    private fun displayName(a: Activity, uri: Uri): String =
        a.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use {
            if (it.moveToFirst()) it.getString(0) else null
        } ?: uri.lastPathSegment ?: ""
}

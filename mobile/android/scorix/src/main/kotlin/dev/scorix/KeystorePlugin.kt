package dev.scorix

import android.content.pm.PackageManager
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import android.util.Base64
import org.json.JSONObject
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The key needs no user auth and survives new fingerprints: the app gates access with its
 * own prompt, and a key that dies on an enrollment change would take every secret with it.
 */
internal class KeystorePlugin : ScorixPlugin {
    override val name = "keystore"

    override fun call(method: String, args: JSONObject, call: PluginCall) {
        val alias = "scorix." + args.getString("alias")
        when (method) {
            "seal" -> {
                val cipher = Cipher.getInstance(TRANSFORM)
                cipher.init(Cipher.ENCRYPT_MODE, key(alias, call))
                val sealed = cipher.iv + cipher.doFinal(decode(args.getString("data")))
                call.resolve(JSONObject().put("data", encode(sealed)))
            }
            "open" -> {
                val sealed = decode(args.getString("data"))
                val ks = keystore()
                val k = ks.getKey(alias, null) as? SecretKey
                    ?: return call.reject("not_found", "no Keystore key $alias")
                val cipher = Cipher.getInstance(TRANSFORM)
                cipher.init(Cipher.DECRYPT_MODE, k, GCMParameterSpec(128, sealed, 0, IV_LEN))
                val plain = cipher.doFinal(sealed, IV_LEN, sealed.size - IV_LEN)
                call.resolve(JSONObject().put("data", encode(plain)))
            }
            "delete" -> {
                keystore().deleteEntry(alias)
                call.resolve()
            }
            else -> call.reject("no_method", "keystore has no method $method")
        }
    }

    private fun keystore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    private fun key(alias: String, call: PluginCall): SecretKey {
        (keystore().getKey(alias, null) as? SecretKey)?.let { return it }
        val strongBox = Build.VERSION.SDK_INT >= Build.VERSION_CODES.P &&
            call.activity?.packageManager?.hasSystemFeature(PackageManager.FEATURE_STRONGBOX_KEYSTORE) == true
        return try {
            generate(alias, strongBox)
        } catch (e: Exception) {
            if (!strongBox || Build.VERSION.SDK_INT < Build.VERSION_CODES.P || e !is StrongBoxUnavailableException) throw e
            generate(alias, false)
        }
    }

    private fun generate(alias: String, strongBox: Boolean): SecretKey {
        val spec = KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
        if (strongBox && Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) spec.setIsStrongBoxBacked(true)
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(spec.build())
        return gen.generateKey()
    }

    private companion object {
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_LEN = 12
    }
}

internal fun encode(b: ByteArray): String = Base64.encodeToString(b, Base64.NO_WRAP)

internal fun decode(s: String): ByteArray = Base64.decode(s, Base64.DEFAULT)

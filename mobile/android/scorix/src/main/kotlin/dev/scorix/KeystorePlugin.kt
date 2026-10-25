package dev.scorix

import android.content.pm.PackageManager
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The key needs no user auth and survives new fingerprints: the app gates access with its
 * own prompt, and a key that dies on an enrollment change would take every secret with it.
 */
internal class KeystorePlugin : KeystorePluginBase() {
    override fun seal(req: KeystoreData, reply: Reply<Sealed>) {
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.ENCRYPT_MODE, key("scorix." + req.alias, reply.call))
        reply.resolve(Sealed(data = cipher.iv + cipher.doFinal(req.data)))
    }

    override fun open(req: KeystoreData, reply: Reply<Sealed>) {
        val alias = "scorix." + req.alias
        val sealed = req.data
        val k = keystore().getKey(alias, null) as? SecretKey
            ?: return reply.reject("not_found", "no Keystore key $alias")
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.DECRYPT_MODE, k, GCMParameterSpec(128, sealed, 0, IV_LEN))
        reply.resolve(Sealed(data = cipher.doFinal(sealed, IV_LEN, sealed.size - IV_LEN)))
    }

    override fun delete(req: KeystoreAlias, reply: Reply<Empty>) {
        keystore().deleteEntry("scorix." + req.alias)
        reply.resolve(Empty())
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

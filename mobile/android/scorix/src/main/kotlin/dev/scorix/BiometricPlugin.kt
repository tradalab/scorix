package dev.scorix

import android.app.KeyguardManager
import android.content.Context
import android.hardware.biometrics.BiometricManager
import android.hardware.biometrics.BiometricPrompt
import android.os.Build
import android.os.CancellationSignal

/** Platform APIs only: androidx.biometric needs a FragmentActivity, which the host is not. */
internal class BiometricPlugin : BiometricPluginBase() {
    override fun status(req: Empty, reply: Reply<BiometricStatus>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "biometric needs an Activity on screen")
        reply.resolve(BiometricStatus(biometric = hasBiometric(a), deviceCredential = keyguard(a).isDeviceSecure))
    }

    override fun authenticate(req: AuthenticateRequest, reply: Reply<Empty>) {
        val a = reply.call.activity ?: return reply.reject("no_activity", "biometric needs an Activity on screen")
        authenticate(a, req, reply.call)
    }

    private fun keyguard(c: Context) = c.getSystemService(KeyguardManager::class.java)

    private fun hasBiometric(c: Context): Boolean = when {
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.R ->
            c.getSystemService(BiometricManager::class.java)
                .canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG) == BiometricManager.BIOMETRIC_SUCCESS
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q ->
            @Suppress("DEPRECATION")
            c.getSystemService(BiometricManager::class.java).canAuthenticate() == BiometricManager.BIOMETRIC_SUCCESS
        else -> false
    }

    private fun authenticate(a: android.app.Activity, req: AuthenticateRequest, call: PluginCall) {
        val title = req.title.ifEmpty { a.title?.toString() ?: "" }
        val subtitle = req.subtitle
        val allowCredential = req.allowDeviceCredential
        val bio = hasBiometric(a)
        val credential = allowCredential && keyguard(a).isDeviceSecure

        if (!bio && !credential) {
            return call.reject("unavailable", "no biometric enrolled and no device credential allowed or set")
        }
        // From API 29 the prompt itself asks for the PIN, biometrics or not.
        // Below that it cannot, so the system lock screen asks instead.
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            @Suppress("DEPRECATION")
            val intent = keyguard(a).createConfirmDeviceCredentialIntent(title, subtitle.ifEmpty { null })
                ?: return call.reject("unavailable", "no device credential set")
            call.startForResult(intent) { code, _ ->
                if (code == android.app.Activity.RESULT_OK) call.resolve() else call.reject("canceled", "the user canceled")
            }
            return
        }

        val b = BiometricPrompt.Builder(a).setTitle(title)
        if (subtitle.isNotEmpty()) b.setSubtitle(subtitle)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            var auth = if (bio) BiometricManager.Authenticators.BIOMETRIC_STRONG else 0
            if (credential) auth = auth or BiometricManager.Authenticators.DEVICE_CREDENTIAL
            b.setAllowedAuthenticators(auth)
        } else {
            @Suppress("DEPRECATION")
            b.setDeviceCredentialAllowed(credential)
        }
        val cancel = CancellationSignal()
        // The prompt refuses to build without a way out when the PIN is not one.
        if (!credential) {
            b.setNegativeButton(req.cancelLabel.ifEmpty { a.getString(android.R.string.cancel) }, a.mainExecutor) { _, _ ->
                call.reject("canceled", "the user canceled")
            }
        }
        call.onCancel { cancel.cancel() }
        // Without a biometric the PIN screen is a full Activity over the app, not a dialog.
        call.coverScreen()
        b.build().authenticate(cancel, a.mainExecutor, object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) = call.resolve()

            override fun onAuthenticationError(code: Int, msg: CharSequence) = call.reject(errorCode(code), msg.toString())
        })
    }

    private fun errorCode(code: Int) = when (code) {
        BiometricPrompt.BIOMETRIC_ERROR_USER_CANCELED,
        BiometricPrompt.BIOMETRIC_ERROR_CANCELED -> "canceled"
        BiometricPrompt.BIOMETRIC_ERROR_LOCKOUT,
        BiometricPrompt.BIOMETRIC_ERROR_LOCKOUT_PERMANENT -> "lockout"
        BiometricPrompt.BIOMETRIC_ERROR_HW_UNAVAILABLE,
        BiometricPrompt.BIOMETRIC_ERROR_HW_NOT_PRESENT,
        BiometricPrompt.BIOMETRIC_ERROR_NO_BIOMETRICS,
        BiometricPrompt.BIOMETRIC_ERROR_NO_DEVICE_CREDENTIAL -> "unavailable"
        else -> "failed"
    }
}

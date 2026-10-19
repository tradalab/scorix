package dev.scorix

import android.app.Activity
import android.content.Intent
import android.content.res.Configuration
import android.os.Build
import android.os.Bundle
import android.window.OnBackInvokedCallback
import android.window.OnBackInvokedDispatcher

/** Launcher Activity (or base class) for the app's Go main package built with -buildmode=c-shared. */
open class ScorixActivity : Activity() {
    /** Name of the Go library in jniLibs, without the lib prefix and .so suffix. */
    protected open val libraryName = "scorixapp"

    protected open fun plugins(): List<ScorixPlugin> = emptyList()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        ScorixHost.register(plugins())
        ScorixHost.attach(this, libraryName)
        // Android 16 with target 36 no longer calls onBackPressed.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(
                OnBackInvokedDispatcher.PRIORITY_DEFAULT,
                OnBackInvokedCallback { back() },
            )
        }
        // A recreated Activity gets its old intent back, a link already delivered.
        if (savedInstanceState == null) ScorixHost.openIntent(this, intent)
    }

    // singleTask: a link tapped while running reaches the live Activity here, not a second one.
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        ScorixHost.openIntent(this, intent)
    }

    @Deprecated("The platform Activity has no other way to deliver results")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (!ScorixHost.onActivityResult(requestCode, resultCode, data)) {
            @Suppress("DEPRECATION")
            super.onActivityResult(requestCode, resultCode, data)
        }
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        ScorixHost.configurationChanged(this)
    }

    override fun onStart() {
        super.onStart()
        ScorixHost.lifecycle(this, ScorixNative.FOREGROUND)
    }

    override fun onStop() {
        super.onStop()
        ScorixHost.lifecycle(this, ScorixNative.BACKGROUND)
    }

    @Deprecated("The only Back callback below API 33")
    override fun onBackPressed() = back()

    private fun back() {
        if (ScorixHost.goBack()) return
        @Suppress("DEPRECATION")
        super.onBackPressed()
    }

    @Deprecated("Still the one callback every API level delivers")
    override fun onLowMemory() {
        super.onLowMemory()
        ScorixHost.lifecycle(this, ScorixNative.LOW_MEMORY)
    }

    override fun onDestroy() {
        ScorixHost.detach(this)
        super.onDestroy()
    }
}

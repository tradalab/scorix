plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// IDs, versions, signing and build types come from scorix.yaml through this file, which
// `scorix mobile sync` rewrites. Set something below it only where the app differs.
apply(from = rootProject.file(".scorix/scorix.gradle"))

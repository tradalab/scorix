import java.util.Properties

plugins {
    id("com.android.application")
}

// Signing comes from the environment so the keystore stays out of git; without it release is unsigned.
val keystore: String? = System.getenv("SCORIX_ANDROID_KEYSTORE")

// Written by `scorix mobile sync` from scorix.yaml; the literals below cover a build that never ran sync.
val scorixVersion = Properties().apply {
    rootProject.file(".scorix/version.properties").takeIf { it.exists() }?.reader()?.use { load(it) }
}

android {
    namespace = "{{ .AppID }}"
    compileSdk = 36

    defaultConfig {
        applicationId = "{{ .AppID }}"
        minSdk = 24
        targetSdk = 36
        versionCode = scorixVersion.getProperty("versionCode", "{{ .VersionCode }}").toInt()
        versionName = scorixVersion.getProperty("versionName", "{{ .Version }}")
    }

    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = System.getenv("SCORIX_ANDROID_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("SCORIX_ANDROID_KEY_ALIAS")
                keyPassword = System.getenv("SCORIX_ANDROID_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"))
            if (keystore != null) signingConfig = signingConfigs.getByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    packaging { jniLibs { useLegacyPackaging = false } }
}

dependencies {
    implementation(project(":scorix"))
}

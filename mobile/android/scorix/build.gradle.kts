plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.scorix"
    compileSdk = 36

    defaultConfig {
        minSdk = 24
        consumerProguardFiles("consumer-rules.pro")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
        // ScorixPlugin's lifecycle defaults become Java default methods, so a Java plugin need not write them.
        freeCompilerArgs += "-Xjvm-default=all"
    }
}

dependencies {
    // WebMessageListener and document-start scripts ship in androidx.webkit, not the platform SDK.
    implementation("androidx.webkit:webkit:1.12.1")
    // Pure Java and offline: QR decoding without Play services.
    implementation("com.google.zxing:core:3.5.3")
}

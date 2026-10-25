# The Go library calls these by name over JNI; R8 cannot see those calls.
-keep class dev.scorix.ScorixNative { *; }
-keep class dev.scorix.ScorixHost { void fromGo(int, byte[]); }
# Looked up by name in ScorixPlugin.kt, so it is not referenced anywhere R8 can see.
-keep class dev.scorix.app.ScorixPlugins { public static java.util.List create(android.content.Context); }

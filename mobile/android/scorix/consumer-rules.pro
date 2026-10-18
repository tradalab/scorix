# The Go library calls these by name over JNI; R8 cannot see those calls.
-keep class dev.scorix.ScorixNative { *; }
-keep class dev.scorix.ScorixHost { void fromGo(int, byte[]); }

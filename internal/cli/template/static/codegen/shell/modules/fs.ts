
export const fs = {
  configDir: () => scorix.invoke<string>("mod:fs:ConfigDir"),
  dataDir: () => scorix.invoke<string>("mod:fs:DataDir"),
  cacheDir: () => scorix.invoke<string>("mod:fs:CacheDir"),
  logDir: () => scorix.invoke<string>("mod:fs:LogDir"),
  tempDir: () => scorix.invoke<string>("mod:fs:TempDir"),
};

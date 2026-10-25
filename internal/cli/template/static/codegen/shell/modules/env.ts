
export interface EnvInfo {
  platform: string;
  arch: string;
  appName: string;
  appVersion: string;
  locale: string;
  darkMode: boolean | null;
}

export const env = {
  info: () => scorix.invoke<EnvInfo | null>("mod:env:Info"),
};


export interface BrowserOpenUrlRequest {
  url?: string;
}

export interface BrowserPathRequest {
  path?: string;
}

export const browser = {
  openUrl: (params?: BrowserOpenUrlRequest) => scorix.invoke<void>("mod:browser:OpenUrl", params),
  revealPath: (params?: BrowserPathRequest) => scorix.invoke<void>("mod:browser:RevealPath", params),
  openPath: (params?: BrowserPathRequest) => scorix.invoke<void>("mod:browser:OpenPath", params),
};


export interface BrowserOpenUrlRequest {
  url?: string;
}

export interface BrowserPathRequest {
  path?: string;
}

export const browser = {
  openUrl: (params?: BrowserOpenUrlRequest) => scorix.invoke<unknown>("mod:browser:OpenUrl", params),
  revealPath: (params?: BrowserPathRequest) => scorix.invoke<unknown>("mod:browser:RevealPath", params),
  openPath: (params?: BrowserPathRequest) => scorix.invoke<unknown>("mod:browser:OpenPath", params),
};

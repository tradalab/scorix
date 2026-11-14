
/** Opens url in the user's browser: a new tab in web mode, where the module would open it on the server. Never throws. */
export function openExternal(url: string): void {
  if (isWebMode()) {
    window.open(url, "_blank", "noopener,noreferrer");
    return;
  }
  void webModeReady()
    .then(web => (web ? void window.open(url, "_blank", "noopener,noreferrer") : browser.openUrl({ url })))
    .catch(() => {});
}

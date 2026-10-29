
export interface ShareRequest {
  title?: string;
  text?: string;
  url?: string;
  dialogTitle?: string;
  fileName?: string;
  mime?: string;
  data?: string;
}

export const share = {
  /** Share returns once the sheet is up: Android does not report whether anything was sent. */
  share: (params?: ShareRequest) => scorix.invoke<string>("mod:share:Share", params),
};

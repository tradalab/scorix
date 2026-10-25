
export interface UpdaterResult {
  has_update: boolean;
  new_version: string;
  notes: string;
  artifact_urls: string[];
  sig_base64: string;
  elevate: boolean;
  local_path: string;
}

export const updater = {
  checkForUpdate: () => scorix.invoke<UpdaterResult | null>("mod:updater:CheckForUpdate"),
  fullUpdate: () => scorix.invoke<UpdaterResult | null>("mod:updater:FullUpdate"),
};


export const scanner = {
  scanQR: () => scorix.invoke<string>("mod:scanner:ScanQR"),
  /** ScanImage covers what the camera cannot: a code already on this phone's own screen. */
  scanImage: () => scorix.invoke<string>("mod:scanner:ScanImage"),
};

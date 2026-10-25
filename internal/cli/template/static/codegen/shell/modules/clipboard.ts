
export interface ClipboardWriteRequest {
  text?: string;
  sensitive?: boolean;
  clearAfterMs?: number;
}

export interface ClipboardReadImageResponse {
  png: string;
}

export interface ClipboardWriteImageRequest {
  png?: string;
}

export const clipboard = {
  read: () => scorix.invoke<string>("mod:clipboard:Read"),
  write: (params?: ClipboardWriteRequest) => scorix.invoke<string>("mod:clipboard:Write", params),
  readImage: () => scorix.invoke<ClipboardReadImageResponse | null>("mod:clipboard:ReadImage"),
  writeImage: (params?: ClipboardWriteImageRequest) => scorix.invoke<string>("mod:clipboard:WriteImage", params),
};


export interface GeolocationRequest {
  maxAgeMs?: number;
  timeoutMs?: number;
  coarse?: boolean;
}

export interface GeolocationPosition {
  latitude: number;
  longitude: number;
  accuracyM: number;
  altitudeM: number;
  speedMps: number;
  bearingDeg: number;
  hasMotion: boolean;
  timeMs: number;
  provider: string;
}

export const geolocation = {
  /** Current asks for the permission first. Errors carry code denied, disabled (location off), timeout (no fix within timeoutMs, 30 s when unset) or unavailable. */
  current: (params?: GeolocationRequest) => scorix.invoke<GeolocationPosition>("mod:geolocation:Current", params),
};

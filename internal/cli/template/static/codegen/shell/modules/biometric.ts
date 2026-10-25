
export interface BiometricStatus {
  biometric: boolean;
  deviceCredential: boolean;
}

export interface BiometricPrompt {
  title?: string;
  subtitle?: string;
  allowDeviceCredential?: boolean;
  cancelLabel?: string;
}

export const biometric = {
  status: () => scorix.invoke<BiometricStatus | null>("mod:biometric:Status"),
  /** Fault codes: canceled, lockout, unavailable, failed. */
  authenticate: (params?: BiometricPrompt) => scorix.invoke<string>("mod:biometric:Authenticate", params),
};

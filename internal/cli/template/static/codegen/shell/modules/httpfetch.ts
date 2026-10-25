
export interface HttpFetchRequest {
  url?: string;
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  timeoutMs?: number;
}

export interface HttpFetchResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
  base64: boolean;
}

export const http = {
  fetch: (params?: HttpFetchRequest) => scorix.invoke<HttpFetchResponse | null>("mod:http:Fetch", params),
};

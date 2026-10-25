
export interface SqlxSQLRequest {
  sql?: string;
  args?: unknown[];
}

export interface SqlxExecResult {
  rows_affected: number;
  last_insert_id: number;
}

export interface SqlxPingResult {
  ok: boolean;
  message?: string;
}

export interface SqlxDBStats {
  max_open_connections: number;
  open_connections: number;
  in_use: number;
  idle: number;
  wait_count: number;
}

export const sqlx = {
  query: (params?: SqlxSQLRequest) => scorix.invoke<Record<string, unknown>[]>("mod:sqlx:Query", params),
  exec: (params?: SqlxSQLRequest) => scorix.invoke<SqlxExecResult | null>("mod:sqlx:Exec", params),
  ping: () => scorix.invoke<SqlxPingResult | null>("mod:sqlx:Ping"),
  stats: () => scorix.invoke<SqlxDBStats | null>("mod:sqlx:Stats"),
};

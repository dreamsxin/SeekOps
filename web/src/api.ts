import type { Account, AccountInput, AdminSetupStatus, BalanceSnapshot, ClientConfig, RequestEvent, Stats, VirtualKey } from "./types";

const keyName = "seekops.adminKey";

export const auth = {
  get: () => sessionStorage.getItem(keyName) ?? "",
  set: (value: string) => sessionStorage.setItem(keyName, value),
  clear: () => sessionStorage.removeItem(keyName),
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      "X-Admin-Key": auth.get(),
      ...init?.headers,
    },
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => null);
    const message = payload?.error?.message ?? payload?.error ?? `HTTP ${response.status}`;
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}

export const api = {
  setupStatus: () => request<AdminSetupStatus>("/admin/setup"),
  setup: (body: { api_key: string }) => request<AdminSetupStatus>("/admin/setup", { method: "POST", body: JSON.stringify(body) }),
  rotateAdminKey: (api_key: string) => request<AdminSetupStatus>("/admin/admin-key", { method: "POST", body: JSON.stringify({ api_key }) }),
  stats: () => request<Stats>("/admin/stats"),
  clientConfig: () => request<ClientConfig>("/admin/client-config"),
  accounts: () => request<Account[]>("/admin/accounts"),
  createAccount: (body: AccountInput) => request<Account>("/admin/accounts", { method: "POST", body: JSON.stringify(body) }),
  updateAccount: (id: string, body: AccountInput) => request<Account>(`/admin/accounts/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(body) }),
  checkAccount: (id: string) => request<Account>(`/admin/accounts/${encodeURIComponent(id)}/check`, { method: "POST" }),
  deleteAccount: (id: string) => request<{ id: string; deleted: boolean }>(`/admin/accounts/${encodeURIComponent(id)}`, { method: "DELETE" }),
  keys: () => request<VirtualKey[]>("/admin/virtual-keys"),
  usage: (query = "") => request<RequestEvent[]>(`/admin/usage${query ? `?${query}` : ""}`),
  balances: (query = "") => request<BalanceSnapshot[]>(`/admin/balance-history${query ? `?${query}` : ""}`),
  createKey: (body: unknown) => request<{ key: VirtualKey; secret: string }>("/admin/virtual-keys", { method: "POST", body: JSON.stringify(body) }),
  revokeKey: (id: string) => request<{ id: string; revoked: boolean }>(`/admin/virtual-keys/${encodeURIComponent(id)}/revoke`, { method: "POST" }),
};

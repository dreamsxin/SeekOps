import type { Account, BalanceSnapshot, RequestEvent, Stats, VirtualKey } from "./types";

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
  stats: () => request<Stats>("/admin/stats"),
  accounts: () => request<Account[]>("/admin/accounts"),
  keys: () => request<VirtualKey[]>("/admin/virtual-keys"),
  usage: (query = "") => request<RequestEvent[]>(`/admin/usage${query ? `?${query}` : ""}`),
  balances: (query = "") => request<BalanceSnapshot[]>(`/admin/balance-history${query ? `?${query}` : ""}`),
  createKey: (body: unknown) => request<{ key: VirtualKey; secret: string }>("/admin/virtual-keys", { method: "POST", body: JSON.stringify(body) }),
  revokeKey: (id: string) => request<{ id: string; revoked: boolean }>(`/admin/virtual-keys/${encodeURIComponent(id)}/revoke`, { method: "POST" }),
};

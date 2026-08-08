import type { Account, AccountInput, AccountTestInput, AccountTestResult, AdminSetupStatus, Alert, AlertSettings, AuditLog, BackupCheckResult, BalanceSnapshot, ClientConfig, PriceRule, PriceRuleInput, RequestEvent, SecurityStatus, Stats, UsageSummary, VirtualKey, VirtualKeyInput } from "./types";

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
  security: () => request<SecurityStatus>("/admin/security"),
  rotateMasterKey: () => request<SecurityStatus>("/admin/security/rotate", { method: "POST" }),
  auditLogs: (query = "") => request<AuditLog[]>(`/admin/audit-logs${query ? `?${query}` : ""}`),
  backupCheck: () => request<BackupCheckResult>("/admin/backups/check"),
  downloadBackup: async () => {
    const response = await fetch("/admin/backups/download", { headers: { "X-Admin-Key": auth.get() } });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      throw new Error(payload?.error ?? `HTTP ${response.status}`);
    }
    const disposition = response.headers.get("Content-Disposition") ?? "";
    const filename = disposition.match(/filename="?([^";]+)"?/i)?.[1] ?? "seekops-backup.zip";
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
  },
  prices: () => request<PriceRule[]>("/admin/prices"),
  createPrice: (body: PriceRuleInput) => request<PriceRule>("/admin/prices", { method: "POST", body: JSON.stringify(body) }),
  deletePrice: (id: string) => request<{ id: string; deleted: boolean }>(`/admin/prices/${encodeURIComponent(id)}`, { method: "DELETE" }),
  alerts: () => request<Alert[]>("/admin/alerts"),
  alertSettings: () => request<AlertSettings>("/admin/alerts/settings"),
  updateAlertSettings: (body: AlertSettings) => request<AlertSettings>("/admin/alerts/settings", { method: "PUT", body: JSON.stringify(body) }),
  acknowledgeAlert: (id: string) => request<Alert>(`/admin/alerts/${encodeURIComponent(id)}/acknowledge`, { method: "POST" }),
  silenceAlert: (id: string, minutes?: number) => request<Alert>(`/admin/alerts/${encodeURIComponent(id)}/silence`, { method: "POST", body: JSON.stringify({ minutes }) }),
  resolveAlert: (id: string) => request<Alert>(`/admin/alerts/${encodeURIComponent(id)}/resolve`, { method: "POST" }),
  stats: () => request<Stats>("/admin/stats"),
  clientConfig: () => request<ClientConfig>("/admin/client-config"),
  accounts: () => request<Account[]>("/admin/accounts"),
  createAccount: (body: AccountInput) => request<Account>("/admin/accounts", { method: "POST", body: JSON.stringify(body) }),
  updateAccount: (id: string, body: AccountInput) => request<Account>(`/admin/accounts/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(body) }),
  checkAccount: (id: string) => request<Account>(`/admin/accounts/${encodeURIComponent(id)}/check`, { method: "POST" }),
  testAccount: (id: string, body: AccountTestInput) => request<AccountTestResult>(`/admin/accounts/${encodeURIComponent(id)}/test`, { method: "POST", body: JSON.stringify(body) }),
  deleteAccount: (id: string) => request<{ id: string; deleted: boolean }>(`/admin/accounts/${encodeURIComponent(id)}`, { method: "DELETE" }),
  keys: () => request<VirtualKey[]>("/admin/virtual-keys"),
  usage: (query = "") => request<RequestEvent[]>(`/admin/usage${query ? `?${query}` : ""}`),
  usageSummary: (query = "") => request<UsageSummary>(`/admin/usage/summary${query ? `?${query}` : ""}`),
  exportUsage: async (query = "") => {
    const response = await fetch(`/admin/usage/export${query ? `?${query}` : ""}`, { headers: { "X-Admin-Key": auth.get() } });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "seekops-usage.csv";
    link.click();
    URL.revokeObjectURL(url);
  },
  balances: (query = "") => request<BalanceSnapshot[]>(`/admin/balance-history${query ? `?${query}` : ""}`),
  createKey: (body: VirtualKeyInput) => request<{ key: VirtualKey; secret: string }>("/admin/virtual-keys", { method: "POST", body: JSON.stringify(body) }),
  updateKey: (id: string, body: VirtualKeyInput) => request<VirtualKey>(`/admin/virtual-keys/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(body) }),
  rotateKey: (id: string) => request<{ key: VirtualKey; secret: string }>(`/admin/virtual-keys/${encodeURIComponent(id)}/rotate`, { method: "POST" }),
  revokeKey: (id: string) => request<{ id: string; revoked: boolean }>(`/admin/virtual-keys/${encodeURIComponent(id)}/revoke`, { method: "POST" }),
};

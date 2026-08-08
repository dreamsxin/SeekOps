export interface Usage {
  prompt_tokens: number;
  cache_hit_tokens: number;
  cache_miss_tokens: number;
  completion_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  usage_present: boolean;
}

export interface RequestEvent {
  request_id: string;
  tenant_id: string;
  virtual_key_id: string;
  account_id: string;
  attempts: number;
  model?: string;
  path: string;
  status: number;
  duration_ms: number;
  first_byte_ms: number;
  usage: Usage;
  usage_status: string;
  price_rule_id?: string;
  price_status: "estimated" | "missing" | "usage_missing" | "legacy";
  estimated_cost_cny: number;
  created_at: string;
}

export interface UsageBucket {
  date: string;
  requests: number;
  successes: number;
  errors: number;
  total_tokens: number;
  estimated_cost_cny: number;
  unpriced_requests: number;
}

export interface UsageBreakdown {
  id: string;
  requests: number;
  successes: number;
  errors: number;
  total_tokens: number;
  estimated_cost_cny: number;
  unpriced_requests: number;
}

export interface UsageSummary {
  start: string;
  end: string;
  requests: number;
  successes: number;
  errors: number;
  total_tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
  cache_hit_tokens: number;
  cache_miss_tokens: number;
  estimated_cost_cny: number;
  unpriced_requests: number;
  daily: UsageBucket[];
  by_tenant: UsageBreakdown[];
  by_virtual_key: UsageBreakdown[];
  by_model: UsageBreakdown[];
  by_account: UsageBreakdown[];
}

export interface Stats {
  requests: number;
  successes: number;
  errors: number;
  total_tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
  cache_hit_tokens: number;
  cache_miss_tokens: number;
  estimated_cost_cny: number;
  unpriced_requests: number;
  last_requests: RequestEvent[];
}

export interface PriceRule {
  id: string;
  model: string;
  cache_hit_cny_per_million: number;
  cache_miss_cny_per_million: number;
  output_cny_per_million: number;
  effective_at: string;
  created_at: string;
}

export interface PriceRuleInput {
  model: string;
  cache_hit_cny_per_million: number;
  cache_miss_cny_per_million: number;
  output_cny_per_million: number;
  effective_at: string;
}

export interface AdminSetupStatus {
  initialized: boolean;
}

export interface SecurityStatus {
  encryption_enabled: boolean;
  key_id?: string;
  key_storage: "local_file" | "external" | "disabled";
  key_file?: string;
  rotation_supported: boolean;
}

export interface BalanceInfo {
  currency: string;
  total_balance: string;
  granted_balance: string;
  topped_up_balance: string;
}

export interface Account {
  id: string;
  name: string;
  api_key_prefix: string;
  base_url: string;
  weight: number;
  models: string[];
  enabled: boolean;
  managed: boolean;
  active: number;
  healthy: boolean;
  check_status: "unchecked" | "healthy" | "cooldown" | "unavailable" | "error" | "disabled";
  failures: number;
  balance_available: boolean;
  balances: BalanceInfo[] | null;
  balance_updated_at?: string;
  balance_error?: string;
}

export interface AccountInput {
  id?: string;
  name: string;
  api_key?: string;
  base_url: string;
  weight: number;
  models: string[];
  enabled: boolean;
}

export interface AccountTestInput {
  mode: "models" | "chat";
  model?: string;
}

export interface AccountTestResult {
  account_id: string;
  mode: "models" | "chat";
  ok: boolean;
  status: number;
  latency_ms: number;
  models?: string[];
  model?: string;
  output?: string;
  error?: string;
  tested_at: string;
}

export interface ClientConfig {
  base_url: string;
  anthropic_base_url: string;
  api_key: string;
  api_key_prefix: string;
}

export interface QuotaPolicy {
  requests_per_minute?: number;
  concurrent_requests?: number;
  daily_tokens?: number;
  daily_cost_cny?: number;
}

export interface QuotaUsage {
  date: string;
  requests_this_minute: number;
  active_requests: number;
  daily_tokens: number;
  daily_cost_cny: number;
}

export interface VirtualKey {
  id: string;
  name: string;
  tenant_id: string;
  prefix: string;
  secret: string;
  secret_available: boolean;
  enabled: boolean;
  created_at: string;
  quota: QuotaPolicy;
  usage: QuotaUsage;
}

export interface VirtualKeyInput {
  name: string;
  tenant_id: string;
  enabled: boolean;
  quota: QuotaPolicy;
}

export interface BalanceSnapshot extends BalanceInfo {
  account_id: string;
  available: boolean;
  observed_at: string;
}

export type AlertStatus = "open" | "acknowledged" | "silenced" | "resolved";
export type AlertSeverity = "warning" | "critical";

export interface Alert {
  id: string;
  source_key: string;
  type: "account_check" | "low_balance" | "quota" | "error_rate";
  scope_type: "account" | "virtual_key" | "platform";
  scope_id: string;
  severity: AlertSeverity;
  title: string;
  message: string;
  status: AlertStatus;
  first_seen_at: string;
  last_seen_at: string;
  acknowledged_at?: string;
  silenced_until?: string;
  resolved_at?: string;
}

export interface AlertSettings {
  balance_threshold_cny: number;
  quota_warning_percent: number;
  error_rate_threshold_percent: number;
  error_rate_min_requests: number;
  error_rate_window_minutes: number;
  silence_minutes: number;
}

export interface AuditLog {
  id: number;
  actor: string;
  action: string;
  resource_type: string;
  resource_id: string;
  summary: string;
  metadata: Record<string, unknown>;
  created_at: string;
}

export interface BackupComponent {
  ok: boolean;
  detail: string;
  path?: string;
  key_id?: string;
}

export interface BackupCheckResult {
  ok: boolean;
  checked_at: string;
  sqlite: BackupComponent;
  secrets: BackupComponent;
  issues: string[];
}

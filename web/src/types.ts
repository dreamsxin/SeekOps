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
  model?: string;
  path: string;
  status: number;
  duration_ms: number;
  first_byte_ms: number;
  usage: Usage;
  usage_status: string;
  estimated_cost_cny: number;
  created_at: string;
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
  last_requests: RequestEvent[];
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

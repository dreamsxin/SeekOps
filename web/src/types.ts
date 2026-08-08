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

export interface BalanceInfo {
  currency: string;
  total_balance: string;
  granted_balance: string;
  topped_up_balance: string;
}

export interface Account {
  id: string;
  name: string;
  base_url: string;
  weight: number;
  active: number;
  healthy: boolean;
  failures: number;
  balance_available: boolean;
  balances: BalanceInfo[] | null;
  balance_updated_at?: string;
  balance_error?: string;
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
  enabled: boolean;
  created_at: string;
  quota: QuotaPolicy;
  usage: QuotaUsage;
}

export interface BalanceSnapshot extends BalanceInfo {
  account_id: string;
  available: boolean;
  observed_at: string;
}

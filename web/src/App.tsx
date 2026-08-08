import { FormEvent, useCallback, useEffect, useState } from "react";
import {
  Activity,
  AlertCircle,
  BarChart3,
  Check,
  CircleDollarSign,
  Clipboard,
  Eye,
  EyeOff,
  Gauge,
  KeyRound,
  LogOut,
  Menu,
  Pencil,
  Plug,
  RefreshCw,
  Server,
  Settings,
  ShieldCheck,
  Trash2,
  WalletCards,
  X,
} from "lucide-react";
import { api, auth } from "./api";
import type { Account, AccountInput, AdminSetupStatus, BalanceSnapshot, ClientConfig, QuotaPolicy, RequestEvent, Stats, VirtualKey, VirtualKeyInput } from "./types";

type View = "overview" | "accounts" | "access" | "keys" | "usage" | "balances" | "settings";

const navItems: Array<{ id: View; label: string; icon: typeof Activity }> = [
  { id: "overview", label: "总览", icon: BarChart3 },
  { id: "accounts", label: "上游账号", icon: Server },
  { id: "access", label: "接入配置", icon: Plug },
  { id: "keys", label: "租户密钥", icon: KeyRound },
  { id: "usage", label: "请求账本", icon: Activity },
  { id: "balances", label: "余额历史", icon: WalletCards },
  { id: "settings", label: "设置", icon: Settings },
];

const initialStats: Stats = {
  requests: 0,
  successes: 0,
  errors: 0,
  total_tokens: 0,
  prompt_tokens: 0,
  completion_tokens: 0,
  cache_hit_tokens: 0,
  cache_miss_tokens: 0,
  estimated_cost_cny: 0,
  last_requests: [],
};

export function App() {
  const [adminKey, setAdminKey] = useState(auth.get());
  const [setup, setSetup] = useState<AdminSetupStatus | null>(null);
  const [authorized, setAuthorized] = useState(false);
  const [checking, setChecking] = useState(true);
  const [view, setView] = useState<View>("overview");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [stats, setStats] = useState<Stats>(initialStats);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [clientConfig, setClientConfig] = useState<ClientConfig | null>(null);
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [usage, setUsage] = useState<RequestEvent[]>([]);
  const [balances, setBalances] = useState<BalanceSnapshot[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState("");
  const [accountEditor, setAccountEditor] = useState<Account | "new" | null>(null);
  const [keyEditor, setKeyEditor] = useState<VirtualKey | null>(null);
  const [checkingAccount, setCheckingAccount] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [nextStats, nextAccounts, nextConfig, nextKeys, nextUsage, nextBalances] = await Promise.all([
        api.stats(),
        api.accounts(),
        api.clientConfig(),
        api.keys(),
        api.usage("limit=100"),
        api.balances("limit=100"),
      ]);
      setStats(nextStats);
      setAccounts(nextAccounts);
      setClientConfig(nextConfig);
      setKeys(nextKeys);
      setUsage(nextUsage);
      setBalances(nextBalances);
      if (!authorized && nextAccounts.length === 0) setView("accounts");
      setAuthorized(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "加载失败");
      if (!authorized) auth.clear();
    } finally {
      setLoading(false);
      setChecking(false);
    }
  }, [authorized]);

  useEffect(() => {
    api.setupStatus().then((status) => {
      setSetup(status);
      if (status.initialized && auth.get()) refresh();
      else {
        if (!status.initialized && !auth.get()) setAdminKey("admin");
        setChecking(false);
      }
    }).catch((err) => {
      setError(err instanceof Error ? err.message : "无法读取初始化状态");
      setChecking(false);
    });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const submitAdminKey = async (event: FormEvent) => {
    event.preventDefault();
    setLoading(true);
    setError("");
    try {
      const value = adminKey.trim();
      if (!value) throw new Error("请输入管理员 API Key");
      if (setup?.initialized) {
        auth.set(value);
      } else {
        await api.setup({ api_key: value });
        setSetup({ initialized: true });
        auth.set(value);
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "验证失败");
      setChecking(false);
    } finally {
      setLoading(false);
    }
  };

  const rotateAdminKey = async (value: string) => {
    const nextKey = value.trim();
    if (!nextKey) throw new Error("请输入新的管理员 API Key");
    await api.rotateAdminKey(nextKey);
    auth.set(nextKey);
    setAdminKey(nextKey);
  };

  const logout = () => {
    auth.clear();
    setAdminKey("");
    setAuthorized(false);
  };

  if (checking) return <div className="boot"><div className="spinner" />正在连接 SeekOps</div>;
  if (!authorized) return <Login setup={!setup?.initialized} value={adminKey} onChange={setAdminKey} onSubmit={submitAdminKey} error={error} loading={loading} />;

  const titles: Record<View, [string, string]> = {
    overview: ["运行总览", "代理流量、成本与健康状态"],
    accounts: ["上游账号", "DeepSeek 账号健康与余额状态"],
    access: ["接入配置", "OpenAI 与 Anthropic 兼容地址"],
    keys: ["租户密钥", "凭据、配额与实时用量"],
    usage: ["请求账本", "持久化调用明细与 Token 用量"],
    balances: ["余额历史", "上游账号余额快照"],
    settings: ["系统设置", "管理员访问凭据与本地运行配置"],
  };

  return (
    <div className="shell">
      <aside className={`sidebar ${sidebarOpen ? "open" : ""}`}>
        <div className="brand"><div className="brand-mark">S</div><div><strong>SeekOps</strong><span>API Control Plane</span></div></div>
        <nav>
          {navItems.map((item) => {
            const Icon = item.icon;
            return <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => { setView(item.id); setSidebarOpen(false); }}><Icon size={18} /><span>{item.label}</span></button>;
          })}
        </nav>
        <div className="sidebar-footer"><span className="status-dot" />本地 SQLite 已连接</div>
      </aside>
      {sidebarOpen && <button className="scrim" aria-label="关闭菜单" onClick={() => setSidebarOpen(false)} />}
      <main>
        <header className="topbar">
          <button className="icon-button mobile-menu" title="打开菜单" onClick={() => setSidebarOpen(true)}><Menu size={20} /></button>
          <div className="page-title"><h1>{titles[view][0]}</h1><p>{titles[view][1]}</p></div>
          <div className="top-actions">
            <button className="icon-button" title="刷新数据" onClick={refresh} disabled={loading}><RefreshCw className={loading ? "spin" : ""} size={18} /></button>
            <button className="icon-button" title="退出" onClick={logout}><LogOut size={18} /></button>
          </div>
        </header>
        <div className="content">
          {error && <div className="error-banner"><AlertCircle size={17} />{error}</div>}
          {view === "overview" && <Overview stats={stats} accounts={accounts} usage={usage} />}
          {view === "accounts" && <Accounts
            accounts={accounts}
            checkingAccount={checkingAccount}
            onCreate={() => setAccountEditor("new")}
            onEdit={(account) => setAccountEditor(account)}
            onCheck={async (account) => {
              setError("");
              setCheckingAccount(account.id);
              try {
                const checked = await api.checkAccount(account.id);
                setAccounts((current) => current.map((item) => item.id === checked.id ? checked : item));
              } catch (err) {
                setError(err instanceof Error ? err.message : "账号检测失败");
              } finally {
                setCheckingAccount("");
              }
            }}
            onToggle={async (account) => { setError(""); try { await api.updateAccount(account.id, accountPayload(account, !account.enabled)); await refresh(); } catch (err) { setError(err instanceof Error ? err.message : "更新账号失败"); } }}
            onDelete={async (account) => { if (confirm(`删除账号 ${account.name}？`)) { setError(""); try { await api.deleteAccount(account.id); await refresh(); } catch (err) { setError(err instanceof Error ? err.message : "删除账号失败"); } } }}
          />}
          {view === "access" && <AccessConfig config={clientConfig} />}
          {view === "keys" && <Keys keys={keys} onCreate={() => { setCreatedSecret(""); setCreateOpen(true); }} onEdit={setKeyEditor} />}
          {view === "usage" && <Usage events={usage} onFilter={async (query) => { setLoading(true); try { setUsage(await api.usage(query)); } finally { setLoading(false); } }} />}
          {view === "balances" && <Balances snapshots={balances} accounts={accounts} onFilter={async (query) => { setLoading(true); try { setBalances(await api.balances(query)); } finally { setLoading(false); } }} />}
          {view === "settings" && <SettingsPage onRotate={rotateAdminKey} />}
        </div>
      </main>
      {createOpen && <CreateKeyModal secret={createdSecret} onClose={() => setCreateOpen(false)} onCreate={async (body) => { const result = await api.createKey(body); setCreatedSecret(result.secret); await refresh(); }} />}
      {keyEditor && <KeyModal keyItem={keyEditor} onClose={() => setKeyEditor(null)} onSave={async (body) => {
        const saved = await api.updateKey(keyEditor.id, body);
        setKeys((current) => current.map((item) => item.id === saved.id ? saved : item));
        setKeyEditor(saved);
        return saved;
      }} onRotate={async () => {
        const result = await api.rotateKey(keyEditor.id);
        setKeys((current) => current.map((item) => item.id === result.key.id ? result.key : item));
        setKeyEditor(result.key);
        return result.key;
      }} onRevoke={async () => {
        await api.revokeKey(keyEditor.id);
        setKeyEditor(null);
        await refresh();
      }} />}
      {accountEditor && <AccountModal account={accountEditor === "new" ? undefined : accountEditor} onClose={() => setAccountEditor(null)} onSave={async (body) => {
        const firstHealthyAccount = !accounts.some((account) => account.healthy);
        const saved = accountEditor === "new" ? await api.createAccount(body) : await api.updateAccount(accountEditor.id, body);
        setAccountEditor(null);
        await refresh();
        if (firstHealthyAccount && saved.healthy) setView("access");
      }} />}
    </div>
  );
}

function Login({ setup, value, onChange, onSubmit, error, loading }: { setup: boolean; value: string; onChange: (v: string) => void; onSubmit: (e: FormEvent) => void; error: string; loading: boolean }) {
  return <div className="login-page"><div className="login-panel"><div className="brand login-brand"><div className="brand-mark">S</div><div><strong>SeekOps</strong><span>DeepSeek API Control Plane</span></div></div><div className="login-copy"><ShieldCheck size={30} /><h1>{setup ? "初始化管理员 Key" : "管理控制台"}</h1><p>{setup ? "首次运行，请设置管理 API Key。默认值为 admin，生产环境建议改为长随机字符串。" : "使用管理员 API Key 进入本地控制台。"}</p></div><form onSubmit={onSubmit}><label>管理员 API Key<input autoFocus type="password" value={value} onChange={(e) => onChange(e.target.value)} placeholder="admin" autoComplete={setup ? "new-password" : "current-password"} required /></label>{error && <p className="form-error">{error}</p>}<button className="primary full" disabled={loading}>{loading ? (setup ? "正在初始化" : "正在验证") : (setup ? "保存并进入" : "进入控制台")}</button></form></div></div>;
}

function SettingsPage({ onRotate }: { onRotate: (value: string) => Promise<void> }) {
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      await onRotate(value);
      setValue("");
      setSaved(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "轮换失败");
    } finally {
      setBusy(false);
    }
  };
  return <div className="panel settings-panel"><PanelHead title="管理员 API Key" subtitle="轮换本地管理控制台的访问凭据" /><form className="settings-form" onSubmit={submit}><label>新的管理员 API Key<input type="password" value={value} onChange={(e) => setValue(e.target.value)} placeholder="输入新的长随机字符串" required autoComplete="new-password" /></label><p className="settings-note">保存后旧 Key 会立即失效，当前浏览器会自动切换到新 Key。</p>{error && <p className="form-error">{error}</p>}{saved && <p className="form-success">管理员 API Key 已更新。</p>}<button className="primary" disabled={busy}>{busy ? "正在保存" : "保存新 Key"}</button></form></div>;
}

function Overview({ stats, accounts, usage }: { stats: Stats; accounts: Account[]; usage: RequestEvent[] }) {
  const hitRate = stats.prompt_tokens ? (stats.cache_hit_tokens / stats.prompt_tokens) * 100 : 0;
  const successRate = stats.requests ? (stats.successes / stats.requests) * 100 : 0;
  return <div className="stack">
    <section className="metric-grid">
      <Metric icon={Activity} label="总请求" value={formatNumber(stats.requests)} detail={`${successRate.toFixed(1)}% 成功`} tone="green" />
      <Metric icon={Gauge} label="Token 总量" value={formatCompact(stats.total_tokens)} detail={`输入 ${formatCompact(stats.prompt_tokens)} · 输出 ${formatCompact(stats.completion_tokens)}`} tone="blue" />
      <Metric icon={CircleDollarSign} label="预估费用" value={`¥ ${stats.estimated_cost_cny.toFixed(4)}`} detail="按当前配置价格" tone="amber" />
      <Metric icon={Server} label="健康账号" value={`${accounts.filter((a) => a.healthy).length} / ${accounts.length}`} detail={`${accounts.reduce((sum, a) => sum + a.active, 0)} 个活跃请求`} tone="red" />
    </section>
    <section className="overview-grid">
      <div className="panel"><PanelHead title="缓存与错误" subtitle="输入 Token 构成" /><div className="cache-block"><div className="donut" style={{ "--value": `${hitRate * 3.6}deg` } as React.CSSProperties}><strong>{hitRate.toFixed(1)}%</strong><span>缓存命中</span></div><div className="legend"><Legend color="green" label="缓存命中" value={formatCompact(stats.cache_hit_tokens)} /><Legend color="blue" label="缓存未命中" value={formatCompact(stats.cache_miss_tokens)} /><Legend color="red" label="失败请求" value={formatNumber(stats.errors)} /></div></div></div>
      <div className="panel"><PanelHead title="最近请求" subtitle="最新 6 条代理调用" /><RequestTable events={usage.slice(0, 6)} compact /></div>
    </section>
  </div>;
}

function Metric({ icon: Icon, label, value, detail, tone }: { icon: typeof Activity; label: string; value: string; detail: string; tone: string }) {
  return <div className="metric"><div className={`metric-icon ${tone}`}><Icon size={19} /></div><div><span>{label}</span><strong>{value}</strong><small>{detail}</small></div></div>;
}

function Accounts({ accounts, checkingAccount, onCreate, onEdit, onCheck, onToggle, onDelete }: { accounts: Account[]; checkingAccount: string; onCreate: () => void; onEdit: (account: Account) => void; onCheck: (account: Account) => Promise<void>; onToggle: (account: Account) => Promise<void>; onDelete: (account: Account) => Promise<void> }) {
  return <div className="panel"><PanelHead title="账号池" subtitle={`${accounts.length} 个上游账号`} action={<button className="primary" onClick={onCreate}><Server size={16} />添加账号</button>} /><div className="table-wrap account-table"><table><thead><tr><th>账号</th><th>状态</th><th>余额</th><th>权重 / 活跃</th><th>来源</th><th></th></tr></thead><tbody>{accounts.map((a) => <tr key={a.id}><td><strong>{a.name}</strong><small>{a.id} · {a.api_key_prefix || "无 Key"}</small></td><td><Status ok={a.healthy} pending={a.check_status === "unchecked" || a.check_status === "disabled"} label={accountStatusLabel(a)} /></td><td>{a.balances?.length ? a.balances.map((b) => <div key={b.currency} className="money">{b.currency} {b.total_balance}</div>) : <span className="muted">暂无快照</span>}{a.balance_updated_at && <small>检测 {formatTime(a.balance_updated_at)}</small>}{a.balance_error && <small className="danger-text">{a.balance_error}</small>}</td><td>{a.weight} / {a.active}</td><td><span className={`source-tag ${a.managed ? "managed" : "env"}`}>{a.managed ? "控制台" : "环境变量"}</span></td><td><div className="row-actions"><button className="icon-button small" title="立即检测" disabled={!a.enabled || checkingAccount === a.id} onClick={() => onCheck(a)}><RefreshCw className={checkingAccount === a.id ? "spin" : ""} size={15} /></button>{a.managed && <><button className="icon-button small" title="编辑账号" onClick={() => onEdit(a)}><Pencil size={15} /></button><label className="switch" title={a.enabled ? "停用账号" : "启用账号"}><input type="checkbox" checked={a.enabled} onChange={() => onToggle(a)} /><span /></label><button className="icon-button small danger-icon" title="删除账号" onClick={() => onDelete(a)}><Trash2 size={15} /></button></>}</div></td></tr>)}</tbody></table>{!accounts.length && <Empty label="尚未配置上游账号" />}</div></div>;
}

function AccessConfig({ config }: { config: ClientConfig | null }) {
  const [revealed, setRevealed] = useState(false);
  const [copied, setCopied] = useState("");
  const [protocol, setProtocol] = useState<"openai" | "anthropic">("openai");
  if (!config) return null;
  const command = protocol === "openai" ? [
    `curl ${config.base_url}/chat/completions \\`,
    `  -H "Authorization: Bearer $SEEKOPS_API_KEY" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}]}'`,
  ].join("\n") : [
    `curl ${config.anthropic_base_url}/v1/messages \\`,
    `  -H "x-api-key: $SEEKOPS_API_KEY" \\`,
    `  -H "anthropic-version: 2023-06-01" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{"model":"deepseek-v4-flash","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}'`,
  ].join("\n");
  const copy = async (key: string, value: string) => {
    await navigator.clipboard.writeText(value);
    setCopied(key);
    window.setTimeout(() => setCopied(""), 1600);
  };
  return <div className="panel access-panel"><PanelHead title="客户端接入" subtitle="OpenAI 与 Anthropic 兼容地址及平台凭据" /><div className="access-content"><div className="access-fields"><div className="access-field"><span>OpenAI Base URL</span><div className="access-value"><code>{config.base_url}</code><button className="icon-button small" title="复制 OpenAI Base URL" onClick={() => copy("openai-base", config.base_url)}>{copied === "openai-base" ? <Check size={15} /> : <Clipboard size={15} />}</button></div></div><div className="access-field"><span>Anthropic Base URL</span><div className="access-value"><code>{config.anthropic_base_url}</code><button className="icon-button small" title="复制 Anthropic Base URL" onClick={() => copy("anthropic-base", config.anthropic_base_url)}>{copied === "anthropic-base" ? <Check size={15} /> : <Clipboard size={15} />}</button></div></div><div className="access-field api-key-field"><span>平台 API Key</span><div className="access-value"><code>{revealed ? config.api_key : `${config.api_key_prefix}••••••••`}</code><button className="icon-button small" title={revealed ? "隐藏 API Key" : "显示 API Key"} onClick={() => setRevealed((value) => !value)}>{revealed ? <EyeOff size={15} /> : <Eye size={15} />}</button><button className="icon-button small" title="复制平台 API Key" onClick={() => copy("key", config.api_key)}>{copied === "key" ? <Check size={15} /> : <Clipboard size={15} />}</button></div></div></div><div className="access-command"><div className="access-command-head"><div className="protocol-tabs"><button className={protocol === "openai" ? "active" : ""} aria-pressed={protocol === "openai"} onClick={() => setProtocol("openai")}>OpenAI</button><button className={protocol === "anthropic" ? "active" : ""} aria-pressed={protocol === "anthropic"} onClick={() => setProtocol("anthropic")}>Anthropic</button></div><button className="icon-button small" title="复制请求示例" onClick={() => copy("command", command)}>{copied === "command" ? <Check size={15} /> : <Clipboard size={15} />}</button></div><pre>{command}</pre></div></div></div>;
}

function Keys({ keys, onCreate, onEdit }: { keys: VirtualKey[]; onCreate: () => void; onEdit: (key: VirtualKey) => void }) {
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [copied, setCopied] = useState("");
  const copy = async (key: VirtualKey) => {
    if (!key.secret_available) return;
    await navigator.clipboard.writeText(key.secret);
    setCopied(key.id);
    window.setTimeout(() => setCopied(""), 1600);
  };
  return <div className="panel key-panel"><PanelHead title="租户密钥" subtitle={`${keys.filter((k) => k.enabled).length} 个启用 · ${keys.length} 个密钥`} action={<button className="primary" onClick={onCreate}><KeyRound size={16} />创建密钥</button>} /><div className="table-wrap key-table"><table><thead><tr><th>名称 / 租户</th><th>密钥</th><th>实时请求</th><th>当日用量</th><th>配额</th><th>状态</th><th></th></tr></thead><tbody>{keys.map((k) => <tr key={k.id}><td><strong>{k.name}</strong><small>{k.tenant_id} · {k.id}</small></td><td><div className="key-secret"><code>{k.secret_available ? (revealed[k.id] ? k.secret : `${k.prefix}••••••••`) : "历史密钥不可恢复"}</code><button className="icon-button small" title={revealed[k.id] ? "隐藏密钥" : "查看密钥"} disabled={!k.secret_available} onClick={() => setRevealed((current) => ({ ...current, [k.id]: !current[k.id] }))}>{revealed[k.id] ? <EyeOff size={15} /> : <Eye size={15} />}</button><button className="icon-button small" title="复制密钥" disabled={!k.secret_available} onClick={() => copy(k)}>{copied === k.id ? <Check size={15} /> : <Clipboard size={15} />}</button></div></td><td><strong>{formatNumber(k.usage.requests_this_minute)} 次/分钟</strong><small>{formatNumber(k.usage.active_requests)} 个处理中</small></td><td><strong>{formatCompact(k.usage.daily_tokens)} Token</strong><small>¥ {k.usage.daily_cost_cny.toFixed(4)}</small></td><td><small className="quota-copy">{quotaText(k.quota)}</small></td><td><Status ok={k.enabled} label={k.enabled ? "启用" : "已停用"} /></td><td><button className="icon-button small" title="管理密钥" onClick={() => onEdit(k)}><Pencil size={15} /></button></td></tr>)}</tbody></table>{!keys.length && <Empty label="尚未创建租户密钥" />}</div></div>;
}

function KeyModal({ keyItem, onClose, onSave, onRotate, onRevoke }: { keyItem: VirtualKey; onClose: () => void; onSave: (body: VirtualKeyInput) => Promise<VirtualKey>; onRotate: () => Promise<VirtualKey>; onRevoke: () => Promise<void> }) {
  const [current, setCurrent] = useState(keyItem);
  const [name, setName] = useState(keyItem.name);
  const [tenant, setTenant] = useState(keyItem.tenant_id);
  const [rpm, setRpm] = useState(String(keyItem.quota.requests_per_minute || ""));
  const [concurrent, setConcurrent] = useState(String(keyItem.quota.concurrent_requests || ""));
  const [tokens, setTokens] = useState(String(keyItem.quota.daily_tokens || ""));
  const [cost, setCost] = useState(String(keyItem.quota.daily_cost_cny || ""));
  const [enabled, setEnabled] = useState(keyItem.enabled);
  const [revealed, setRevealed] = useState(false);
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const payload = (): VirtualKeyInput => ({ name: name.trim(), tenant_id: tenant.trim(), enabled: current.id === "vk-default" ? true : enabled, quota: { requests_per_minute: numberOrZero(rpm), concurrent_requests: numberOrZero(concurrent), daily_tokens: numberOrZero(tokens), daily_cost_cny: numberOrZero(cost) } });
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy("save");
    setError("");
    try {
      const saved = await onSave(payload());
      setCurrent(saved);
      setEnabled(saved.enabled);
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存密钥失败");
    } finally {
      setBusy("");
    }
  };
  const rotate = async () => {
    if (!confirm("轮换后旧密钥会立即失效。继续吗？")) return;
    setBusy("rotate");
    setError("");
    try {
      const rotated = await onRotate();
      setCurrent(rotated);
      setEnabled(true);
      setRevealed(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "轮换密钥失败");
    } finally {
      setBusy("");
    }
  };
  return <div className="modal-backdrop"><div className="modal key-modal"><div className="modal-head"><div><h2>管理租户密钥</h2><p>{current.id} · 创建于 {formatTime(current.created_at)}</p></div><button className="icon-button" title="关闭" onClick={onClose}><X size={19} /></button></div><div className="key-modal-body"><section className="key-credential"><div className="section-label">API Key</div><div className="key-credential-value"><code>{current.secret_available ? (revealed ? current.secret : `${current.prefix}••••••••`) : "此密钥由旧版本创建，当前无法恢复"}</code><button className="icon-button small" title={revealed ? "隐藏密钥" : "查看密钥"} disabled={!current.secret_available} onClick={() => setRevealed((value) => !value)}>{revealed ? <EyeOff size={15} /> : <Eye size={15} />}</button><button className="icon-button small" title="复制密钥" disabled={!current.secret_available} onClick={async () => { await navigator.clipboard.writeText(current.secret); setCopied(true); window.setTimeout(() => setCopied(false), 1600); }}>{copied ? <Check size={15} /> : <Clipboard size={15} />}</button></div>{!current.secret_available && current.id !== "vk-default" && <p className="settings-note">轮换后会生成可查看的新密钥，旧密钥立即失效。</p>}</section><section><div className="section-label">当前用量</div><div className="key-usage-grid"><div><span>本分钟请求</span><strong>{formatNumber(current.usage.requests_this_minute)}</strong></div><div><span>处理中</span><strong>{formatNumber(current.usage.active_requests)}</strong></div><div><span>今日 Token</span><strong>{formatCompact(current.usage.daily_tokens)}</strong></div><div><span>今日费用</span><strong>¥ {current.usage.daily_cost_cny.toFixed(4)}</strong></div></div></section><form onSubmit={submit} className="create-form key-form"><div className="section-label">租户与配额</div><div className="form-grid"><label>名称<input value={name} onChange={(e) => setName(e.target.value)} required /></label><label>租户 ID<input value={tenant} onChange={(e) => setTenant(e.target.value)} required /></label><label>每分钟请求<input type="number" min="0" value={rpm} onChange={(e) => setRpm(e.target.value)} placeholder="不限" /></label><label>最大并发<input type="number" min="0" value={concurrent} onChange={(e) => setConcurrent(e.target.value)} placeholder="不限" /></label><label>每日 Token<input type="number" min="0" value={tokens} onChange={(e) => setTokens(e.target.value)} placeholder="不限" /></label><label>每日费用（元）<input type="number" min="0" step="0.01" value={cost} onChange={(e) => setCost(e.target.value)} placeholder="不限" /></label></div><label className={`check-row ${current.id === "vk-default" ? "disabled" : ""}`}><input type="checkbox" checked={enabled} disabled={current.id === "vk-default"} onChange={(e) => setEnabled(e.target.checked)} />启用密钥{current.id === "vk-default" && "（平台默认 Key 始终启用）"}</label>{error && <p className="form-error">{error}</p>}<div className="modal-actions key-modal-actions">{current.id !== "vk-default" && <><button type="button" className="secondary" disabled={Boolean(busy)} onClick={rotate}><RefreshCw size={15} />{busy === "rotate" ? "正在轮换" : "轮换密钥"}</button><button type="button" className="danger-action" disabled={Boolean(busy)} onClick={async () => { if (!confirm("撤销后客户端将立即无法使用该密钥。继续吗？")) return; setBusy("revoke"); setError(""); try { await onRevoke(); } catch (err) { setError(err instanceof Error ? err.message : "撤销密钥失败"); setBusy(""); } }}>撤销</button></>}<span className="modal-action-spacer" /><button type="button" className="secondary" onClick={onClose}>关闭</button><button className="primary" disabled={Boolean(busy)}>{busy === "save" ? "正在保存" : "保存配置"}</button></div></form></div></div></div>;
}

function accountPayload(account: Account, enabled = account.enabled): AccountInput {
  return { id: account.id, name: account.name, api_key: "", base_url: account.base_url, weight: account.weight, models: account.models ?? [], enabled };
}

function AccountModal({ account, onClose, onSave }: { account?: Account; onClose: () => void; onSave: (body: AccountInput) => Promise<void> }) {
  const [id, setID] = useState(account?.id ?? "");
  const [name, setName] = useState(account?.name ?? "");
  const [apiKey, setAPIKey] = useState("");
  const [baseURL, setBaseURL] = useState(account?.base_url ?? "https://api.deepseek.com");
  const [weight, setWeight] = useState(String(account?.weight ?? 1));
  const [models, setModels] = useState(account?.models?.join(", ") ?? "");
  const [enabled, setEnabled] = useState(account?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await onSave({ id: id.trim() || undefined, name: name.trim(), api_key: apiKey.trim(), base_url: baseURL.trim(), weight: Number(weight) || 1, models: models.split(",").map((item) => item.trim()).filter(Boolean), enabled });
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存账号失败");
    } finally {
      setBusy(false);
    }
  };
  return <div className="modal-backdrop"><div className="modal"><div className="modal-head"><div><h2>{account ? "编辑上游账号" : "添加上游账号"}</h2><p>{account ? "修改账号参数，API Key 留空则保持不变。" : "添加一个可参与请求转发和余额轮询的账号。"}</p></div><button className="icon-button" title="关闭" onClick={onClose}><X size={19} /></button></div><form onSubmit={submit} className="create-form"><div className="form-grid">{!account && <label>账号 ID<input value={id} onChange={(e) => setID(e.target.value)} placeholder="acct-prod" /></label>}<label>名称<input value={name} onChange={(e) => setName(e.target.value)} required placeholder="生产主账号" /></label><label>API Key<input type="password" value={apiKey} onChange={(e) => setAPIKey(e.target.value)} placeholder={account ? "留空保持不变" : "sk-..."} required={!account} autoComplete="new-password" /></label><label>Base URL<input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} required placeholder="https://api.deepseek.com" /></label><label>权重<input type="number" min="1" max="1000" value={weight} onChange={(e) => setWeight(e.target.value)} /></label><label>支持模型<input value={models} onChange={(e) => setModels(e.target.value)} placeholder="deepseek-chat, deepseek-reasoner" /></label></div><label className="check-row"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />启用账号</label>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>取消</button><button className="primary" disabled={busy}>{busy ? "正在保存" : "保存账号"}</button></div></form></div></div>;
}

function Usage({ events, onFilter }: { events: RequestEvent[]; onFilter: (query: string) => void }) {
  const [tenant, setTenant] = useState(""); const [model, setModel] = useState("");
  const submit = (e: FormEvent) => { e.preventDefault(); const q = new URLSearchParams({ limit: "200" }); if (tenant) q.set("tenant_id", tenant); if (model) q.set("model", model); onFilter(q.toString()); };
  return <div className="panel"><PanelHead title="请求事件" subtitle={`显示 ${events.length} 条记录`} action={<form className="filters" onSubmit={submit}><input value={tenant} onChange={(e) => setTenant(e.target.value)} placeholder="租户 ID" /><input value={model} onChange={(e) => setModel(e.target.value)} placeholder="模型" /><button className="secondary">筛选</button></form>} /><RequestTable events={events} /></div>;
}

function RequestTable({ events, compact = false }: { events: RequestEvent[]; compact?: boolean }) {
  return <div className="table-wrap"><table><thead><tr><th>时间 / 请求</th><th>租户</th><th>模型</th><th>状态</th><th>Token</th>{!compact && <><th>首字节</th><th>费用</th></>}</tr></thead><tbody>{events.map((e) => <tr key={e.request_id}><td><span>{formatTime(e.created_at)}</span><small className="mono">{e.request_id.slice(0, 10)}</small></td><td>{e.tenant_id || "-"}</td><td>{e.model || "-"}</td><td><Status ok={e.status >= 200 && e.status < 400} label={String(e.status)} /></td><td>{formatNumber(e.usage.total_tokens)}</td>{!compact && <><td>{e.first_byte_ms} ms</td><td>¥ {e.estimated_cost_cny.toFixed(5)}</td></>}</tr>)}</tbody></table>{!events.length && <Empty label="暂无请求记录" />}</div>;
}

function Balances({ snapshots, accounts, onFilter }: { snapshots: BalanceSnapshot[]; accounts: Account[]; onFilter: (query: string) => void }) {
  const [account, setAccount] = useState("");
  return <div className="panel"><PanelHead title="余额快照" subtitle={`显示 ${snapshots.length} 条记录`} action={<div className="filters"><select value={account} onChange={(e) => { setAccount(e.target.value); onFilter(e.target.value ? `account_id=${encodeURIComponent(e.target.value)}&limit=200` : "limit=200"); }}><option value="">全部账号</option>{accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></div>} /><div className="table-wrap"><table><thead><tr><th>采集时间</th><th>账号</th><th>币种</th><th>总余额</th><th>赠金</th><th>充值余额</th></tr></thead><tbody>{snapshots.map((s, index) => <tr key={`${s.account_id}-${s.observed_at}-${index}`}><td>{formatTime(s.observed_at)}</td><td>{s.account_id}</td><td>{s.currency}</td><td className="money">{s.total_balance}</td><td>{s.granted_balance}</td><td>{s.topped_up_balance}</td></tr>)}</tbody></table>{!snapshots.length && <Empty label="暂无余额快照" />}</div></div>;
}

function CreateKeyModal({ secret, onClose, onCreate }: { secret: string; onClose: () => void; onCreate: (body: VirtualKeyInput) => Promise<void> }) {
  const [name, setName] = useState(""); const [tenant, setTenant] = useState(""); const [rpm, setRpm] = useState(""); const [concurrent, setConcurrent] = useState(""); const [tokens, setTokens] = useState(""); const [cost, setCost] = useState(""); const [busy, setBusy] = useState(false); const [copied, setCopied] = useState(false);
  const submit = async (e: FormEvent) => { e.preventDefault(); setBusy(true); try { await onCreate({ name, tenant_id: tenant, enabled: true, quota: { requests_per_minute: numberOrZero(rpm), concurrent_requests: numberOrZero(concurrent), daily_tokens: numberOrZero(tokens), daily_cost_cny: numberOrZero(cost) } }); } finally { setBusy(false); } };
  return <div className="modal-backdrop"><div className="modal"><div className="modal-head"><div><h2>{secret ? "密钥已创建" : "创建租户密钥"}</h2><p>{secret ? "可在租户密钥菜单中随时查看、复制或轮换。" : "为租户设置独立凭据和用量边界。"}</p></div><button className="icon-button" title="关闭" onClick={onClose}><X size={19} /></button></div>{secret ? <div className="secret-result"><label>租户 API Key</label><div><code>{secret}</code><button className="icon-button" title="复制密钥" onClick={async () => { await navigator.clipboard.writeText(secret); setCopied(true); }} >{copied ? <Check size={18} /> : <Clipboard size={18} />}</button></div><button className="primary full" onClick={onClose}>完成</button></div> : <form onSubmit={submit} className="create-form"><div className="form-grid"><label>名称<input value={name} onChange={(e) => setName(e.target.value)} required placeholder="生产应用" /></label><label>租户 ID<input value={tenant} onChange={(e) => setTenant(e.target.value)} required placeholder="tenant-prod" /></label><label>每分钟请求<input type="number" min="0" value={rpm} onChange={(e) => setRpm(e.target.value)} placeholder="不限" /></label><label>最大并发<input type="number" min="0" value={concurrent} onChange={(e) => setConcurrent(e.target.value)} placeholder="不限" /></label><label>每日 Token<input type="number" min="0" value={tokens} onChange={(e) => setTokens(e.target.value)} placeholder="不限" /></label><label>每日费用（元）<input type="number" min="0" step="0.01" value={cost} onChange={(e) => setCost(e.target.value)} placeholder="不限" /></label></div><div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>取消</button><button className="primary" disabled={busy}>{busy ? "正在创建" : "创建"}</button></div></form>}</div></div>;
}

function PanelHead({ title, subtitle, action }: { title: string; subtitle: string; action?: React.ReactNode }) { return <div className="panel-head"><div><h2>{title}</h2><p>{subtitle}</p></div>{action}</div>; }
function Status({ ok, label, pending = false }: { ok: boolean; label: string; pending?: boolean }) { return <span className={`status ${pending ? "pending" : ok ? "ok" : "bad"}`}><span />{label}</span>; }
function accountStatusLabel(account: Account) {
  switch (account.check_status) {
    case "healthy": return "健康";
    case "unchecked": return "待检测";
    case "cooldown": return "冷却中";
    case "unavailable": return "余额不可用";
    case "disabled": return "已停用";
    default: return "检测失败";
  }
}
function Legend({ color, label, value }: { color: string; label: string; value: string }) { return <div><span className={`legend-dot ${color}`} /><span>{label}</span><strong>{value}</strong></div>; }
function Empty({ label }: { label: string }) { return <div className="empty">{label}</div>; }
function formatNumber(v = 0) { return new Intl.NumberFormat("zh-CN").format(v); }
function formatCompact(v = 0) { return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(v); }
function formatTime(value?: string) { if (!value || value.startsWith("0001-")) return "-"; return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(value)); }
function numberOrZero(value: string) { return value ? Number(value) : 0; }
function quotaText(q: QuotaPolicy) { const parts = []; if (q.requests_per_minute) parts.push(`${q.requests_per_minute} RPM`); if (q.concurrent_requests) parts.push(`${q.concurrent_requests} 并发`); if (q.daily_tokens) parts.push(`${formatCompact(q.daily_tokens)} Token/日`); if (q.daily_cost_cny) parts.push(`¥${q.daily_cost_cny}/日`); return parts.join(" · ") || "不限"; }

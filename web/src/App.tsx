import { FormEvent, useCallback, useEffect, useState } from "react";
import {
  Activity,
  AlertCircle,
  BarChart3,
  Bell,
  BellOff,
  Check,
  CheckCheck,
  CircleDollarSign,
  Clipboard,
  Download,
  Eye,
  EyeOff,
  FlaskConical,
  Gauge,
  KeyRound,
  LogOut,
  Menu,
  Pencil,
  Plug,
  Plus,
  RefreshCw,
  Server,
  Settings,
  ShieldCheck,
  Trash2,
  WalletCards,
  X,
} from "lucide-react";
import { api, auth } from "./api";
import type { Account, AccountInput, AccountTestResult, AdminSetupStatus, Alert, AlertSettings, BalanceSnapshot, ClientConfig, PriceRule, PriceRuleInput, QuotaPolicy, RequestEvent, SecurityStatus, Stats, UsageBreakdown, UsageSummary, VirtualKey, VirtualKeyInput } from "./types";

type View = "overview" | "accounts" | "alerts" | "access" | "keys" | "usage" | "balances" | "settings";

const navItems: Array<{ id: View; label: string; icon: typeof Activity }> = [
  { id: "overview", label: "总览", icon: BarChart3 },
  { id: "accounts", label: "上游账号", icon: Server },
  { id: "alerts", label: "告警中心", icon: Bell },
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
  unpriced_requests: 0,
  last_requests: [],
};

const initialAlertSettings: AlertSettings = {
  balance_threshold_cny: 10,
  quota_warning_percent: 80,
  error_rate_threshold_percent: 20,
  error_rate_min_requests: 10,
  error_rate_window_minutes: 15,
  silence_minutes: 60,
};

function BrandMark() {
  return <img className="brand-mark" src="/console/seekops-mark.svg" alt="SeekOps logo" />;
}

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
  const [usageSummary, setUsageSummary] = useState<UsageSummary | null>(null);
  const [balances, setBalances] = useState<BalanceSnapshot[]>([]);
  const [security, setSecurity] = useState<SecurityStatus | null>(null);
  const [prices, setPrices] = useState<PriceRule[]>([]);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [alertSettings, setAlertSettings] = useState<AlertSettings>(initialAlertSettings);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState("");
  const [accountEditor, setAccountEditor] = useState<Account | "new" | null>(null);
  const [accountTester, setAccountTester] = useState<Account | null>(null);
  const [keyEditor, setKeyEditor] = useState<VirtualKey | null>(null);
  const [checkingAccount, setCheckingAccount] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const defaultUsageQuery = recentUsageQuery();
      const [nextStats, nextAccounts, nextConfig, nextKeys, nextUsage, nextUsageSummary, nextBalances, nextSecurity, nextPrices, nextAlerts, nextAlertSettings] = await Promise.all([
        api.stats(),
        api.accounts(),
        api.clientConfig(),
        api.keys(),
        api.usage(defaultUsageQuery),
        api.usageSummary(defaultUsageQuery),
        api.balances("limit=100"),
        api.security(),
        api.prices(),
        api.alerts(),
        api.alertSettings(),
      ]);
      setStats(nextStats);
      setAccounts(nextAccounts);
      setClientConfig(nextConfig);
      setKeys(nextKeys);
      setUsage(nextUsage);
      setUsageSummary(nextUsageSummary);
      setBalances(nextBalances);
      setSecurity(nextSecurity);
      setPrices(nextPrices);
      setAlerts(nextAlerts);
      setAlertSettings(nextAlertSettings);
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

  useEffect(() => {
    if (!authorized) return;
    const timer = window.setInterval(() => {
      api.alerts().then(setAlerts).catch(() => undefined);
    }, 30_000);
    return () => window.clearInterval(timer);
  }, [authorized]);

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
    alerts: ["告警中心", "需要处理的账号、配额与流量异常"],
    access: ["接入配置", "OpenAI 与 Anthropic 兼容地址"],
    keys: ["租户密钥", "凭据、配额与实时用量"],
    usage: ["请求账本", "持久化调用明细与 Token 用量"],
    balances: ["余额历史", "上游账号余额快照"],
    settings: ["系统设置", "管理员访问凭据与本地运行配置"],
  };
  const openAlertCount = alerts.filter((item) => item.status === "open").length;

  return (
    <div className="shell">
      <aside className={`sidebar ${sidebarOpen ? "open" : ""}`}>
        <div className="brand"><BrandMark /><div><strong>SeekOps</strong><span>API Control Plane</span></div></div>
        <nav>
          {navItems.map((item) => {
            const Icon = item.icon;
            return <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => { setView(item.id); setSidebarOpen(false); }}><Icon size={18} /><span>{item.label}</span>{item.id === "alerts" && openAlertCount > 0 && <b className="nav-badge">{openAlertCount > 99 ? "99+" : openAlertCount}</b>}</button>;
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
            onTest={(account) => setAccountTester(account)}
            onCheck={async (account) => {
              setError("");
              setCheckingAccount(account.id);
              try {
                const checked = await api.checkAccount(account.id);
                setAccounts((current) => current.map((item) => item.id === checked.id ? checked : item));
                setAlerts(await api.alerts());
              } catch (err) {
                setError(err instanceof Error ? err.message : "账号检测失败");
              } finally {
                setCheckingAccount("");
              }
            }}
            onToggle={async (account) => { setError(""); try { await api.updateAccount(account.id, accountPayload(account, !account.enabled)); await refresh(); } catch (err) { setError(err instanceof Error ? err.message : "更新账号失败"); } }}
            onDelete={async (account) => { if (confirm(`删除账号 ${account.name}？`)) { setError(""); try { await api.deleteAccount(account.id); await refresh(); } catch (err) { setError(err instanceof Error ? err.message : "删除账号失败"); } } }}
          />}
          {view === "alerts" && <AlertsPage alerts={alerts} settings={alertSettings} onAcknowledge={async (id) => {
            const updated = await api.acknowledgeAlert(id);
            setAlerts((current) => current.map((item) => item.id === updated.id ? updated : item));
          }} onSilence={async (id) => {
            const updated = await api.silenceAlert(id, alertSettings.silence_minutes);
            setAlerts((current) => current.map((item) => item.id === updated.id ? updated : item));
          }} onResolve={async (id) => {
            const updated = await api.resolveAlert(id);
            setAlerts((current) => current.map((item) => item.id === updated.id ? updated : item));
          }} onSaveSettings={async (next) => {
            const saved = await api.updateAlertSettings(next);
            setAlertSettings(saved);
            setAlerts(await api.alerts());
          }} />}
          {view === "access" && <AccessConfig config={clientConfig} />}
          {view === "keys" && <Keys keys={keys} onCreate={() => { setCreatedSecret(""); setCreateOpen(true); }} onEdit={setKeyEditor} />}
          {view === "usage" && <Usage events={usage} summary={usageSummary} onApply={async (query) => { setLoading(true); setError(""); try { const [nextSummary, nextUsage] = await Promise.all([api.usageSummary(query), api.usage(query)]); setUsageSummary(nextSummary); setUsage(nextUsage); } catch (err) { setError(err instanceof Error ? err.message : "加载用量失败"); } finally { setLoading(false); } }} onExport={api.exportUsage} />}
          {view === "balances" && <Balances snapshots={balances} accounts={accounts} onFilter={async (query) => { setLoading(true); try { setBalances(await api.balances(query)); } finally { setLoading(false); } }} />}
          {view === "settings" && <SettingsPage security={security} prices={prices} onCreatePrice={async (body) => {
            const created = await api.createPrice(body);
            setPrices((current) => [created, ...current].sort((a, b) => b.effective_at.localeCompare(a.effective_at)));
            return created;
          }} onDeletePrice={async (id) => {
            await api.deletePrice(id);
            setPrices((current) => current.filter((item) => item.id !== id));
          }} onRotateAdmin={rotateAdminKey} onRotateMaster={async () => {
            const next = await api.rotateMasterKey();
            setSecurity(next);
            return next;
          }} />}
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
      {accountTester && <AccountTestModal account={accountTester} onClose={() => setAccountTester(null)} onRun={(mode, model) => api.testAccount(accountTester.id, { mode, model })} onSync={async (models) => {
        const saved = await api.updateAccount(accountTester.id, { ...accountPayload(accountTester), models });
        setAccounts((current) => current.map((item) => item.id === saved.id ? saved : item));
        setAccountTester(saved);
        return saved;
      }} />}
    </div>
  );
}

function Login({ setup, value, onChange, onSubmit, error, loading }: { setup: boolean; value: string; onChange: (v: string) => void; onSubmit: (e: FormEvent) => void; error: string; loading: boolean }) {
  return <div className="login-page"><div className="login-panel"><div className="brand login-brand"><BrandMark /><div><strong>SeekOps</strong><span>DeepSeek API Control Plane</span></div></div><div className="login-copy"><ShieldCheck size={30} /><h1>{setup ? "初始化管理员 Key" : "管理控制台"}</h1><p>{setup ? "首次运行，请设置管理 API Key。默认值为 admin，生产环境建议改为长随机字符串。" : "使用管理员 API Key 进入本地控制台。"}</p></div><form onSubmit={onSubmit}><label>管理员 API Key<input autoFocus type="password" value={value} onChange={(e) => onChange(e.target.value)} placeholder="admin" autoComplete={setup ? "new-password" : "current-password"} required /></label>{error && <p className="form-error">{error}</p>}<button className="primary full" disabled={loading}>{loading ? (setup ? "正在初始化" : "正在验证") : (setup ? "保存并进入" : "进入控制台")}</button></form></div></div>;
}

function SettingsPage({ security, prices, onCreatePrice, onDeletePrice, onRotateAdmin, onRotateMaster }: { security: SecurityStatus | null; prices: PriceRule[]; onCreatePrice: (value: PriceRuleInput) => Promise<PriceRule>; onDeletePrice: (id: string) => Promise<void>; onRotateAdmin: (value: string) => Promise<void>; onRotateMaster: () => Promise<SecurityStatus> }) {
  const [value, setValue] = useState("");
  const [adminBusy, setAdminBusy] = useState(false);
  const [masterBusy, setMasterBusy] = useState(false);
  const [adminError, setAdminError] = useState("");
  const [masterError, setMasterError] = useState("");
  const [adminSaved, setAdminSaved] = useState(false);
  const [masterSaved, setMasterSaved] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setAdminBusy(true);
    setAdminError("");
    setAdminSaved(false);
    try {
      await onRotateAdmin(value);
      setValue("");
      setAdminSaved(true);
    } catch (err) {
      setAdminError(err instanceof Error ? err.message : "轮换失败");
    } finally {
      setAdminBusy(false);
    }
  };
  const rotateMaster = async () => {
    if (!confirm("轮换后将使用新主密钥重写全部 SQLite 凭据。继续吗？")) return;
    setMasterBusy(true);
    setMasterError("");
    setMasterSaved(false);
    try {
      await onRotateMaster();
      setMasterSaved(true);
    } catch (err) {
      setMasterError(err instanceof Error ? err.message : "主密钥轮换失败");
    } finally {
      setMasterBusy(false);
    }
  };
  const storageLabel = security?.key_storage === "local_file" ? "本地密钥文件" : security?.key_storage === "external" ? "外部主密钥" : "未启用";
  return <div className="settings-stack">
    <section className="panel settings-panel"><PanelHead title="SQLite 凭据加密" subtitle="上游 API Key 与可恢复租户密钥" /><div className="security-content"><div className="security-state"><div className={`security-icon ${security?.encryption_enabled ? "ok" : "bad"}`}><ShieldCheck size={20} /></div><div><strong>{security?.encryption_enabled ? "AES-256-GCM 已启用" : "凭据加密未启用"}</strong><span>{storageLabel}</span></div></div><dl className="security-details"><div><dt>主密钥 ID</dt><dd><code>{security?.key_id || "-"}</code></dd></div><div><dt>密钥位置</dt><dd><code>{security?.key_file || (security?.key_storage === "external" ? "SECRETS_MASTER_KEY" : "-")}</code></dd></div></dl><p className="settings-note">备份 SQLite 时需同时备份密钥文件；缺失或不匹配时服务会拒绝启动。</p>{masterError && <p className="form-error">{masterError}</p>}{masterSaved && <p className="form-success">主密钥已轮换，SQLite 凭据已重新加密。</p>}<button className="secondary" disabled={masterBusy || !security?.rotation_supported} onClick={rotateMaster}><RefreshCw className={masterBusy ? "spin" : ""} size={15} />{masterBusy ? "正在轮换" : "轮换本地主密钥"}</button></div></section>
    <PriceSettings prices={prices} onCreate={onCreatePrice} onDelete={onDeletePrice} />
    <section className="panel settings-panel"><PanelHead title="管理员 API Key" subtitle="本地管理控制台访问凭据" /><form className="settings-form" onSubmit={submit}><label>新的管理员 API Key<input type="password" value={value} onChange={(e) => setValue(e.target.value)} placeholder="输入新的长随机字符串" required autoComplete="new-password" /></label><p className="settings-note">保存后旧 Key 会立即失效，当前浏览器会自动切换到新 Key。</p>{adminError && <p className="form-error">{adminError}</p>}{adminSaved && <p className="form-success">管理员 API Key 已更新。</p>}<button className="primary" disabled={adminBusy}>{adminBusy ? "正在保存" : "保存新 Key"}</button></form></section>
  </div>;
}

function PriceSettings({ prices, onCreate, onDelete }: { prices: PriceRule[]; onCreate: (value: PriceRuleInput) => Promise<PriceRule>; onDelete: (id: string) => Promise<void> }) {
  const [model, setModel] = useState("*");
  const [hit, setHit] = useState("");
  const [miss, setMiss] = useState("");
  const [output, setOutput] = useState("");
  const [effectiveAt, setEffectiveAt] = useState(localDateTimeInput());
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const values = [Number(hit), Number(miss), Number(output)];
    if (values.some((value) => !Number.isFinite(value) || value < 0)) {
      setError("价格必须是大于或等于 0 的数字");
      return;
    }
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      await onCreate({ model: model.trim(), cache_hit_cny_per_million: values[0], cache_miss_cny_per_million: values[1], output_cny_per_million: values[2], effective_at: new Date(effectiveAt).toISOString() });
      setSaved(true);
      setHit("");
      setMiss("");
      setOutput("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存价格版本失败");
    } finally {
      setBusy(false);
    }
  };
  const remove = async (rule: PriceRule) => {
    if (!confirm(`删除 ${rule.model === "*" ? "默认" : rule.model} 的这个价格版本？历史请求费用不会改变。`)) return;
    setDeleting(rule.id);
    setError("");
    try {
      await onDelete(rule.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "删除价格版本失败");
    } finally {
      setDeleting("");
    }
  };
  return <section className="panel settings-panel"><PanelHead title="模型价格版本" subtitle="每百万 Token / 人民币；新版本不会改写历史费用" /><form className="price-form" onSubmit={submit}><label>模型<input value={model} onChange={(event) => setModel(event.target.value)} required placeholder="deepseek-v4-flash 或 *" /></label><label>缓存命中输入<input type="number" min="0" step="0.0001" value={hit} onChange={(event) => setHit(event.target.value)} required placeholder="0.02" /></label><label>缓存未命中输入<input type="number" min="0" step="0.0001" value={miss} onChange={(event) => setMiss(event.target.value)} required placeholder="1.00" /></label><label>输出<input type="number" min="0" step="0.0001" value={output} onChange={(event) => setOutput(event.target.value)} required placeholder="2.00" /></label><label>生效时间<input type="datetime-local" value={effectiveAt} onChange={(event) => setEffectiveAt(event.target.value)} required /></label><button className="primary" disabled={busy}><Plus size={15} />{busy ? "正在保存" : "新增价格版本"}</button><p className="settings-note price-note"><code>*</code> 是未配置专属价格模型的默认规则。缺少匹配价格时，请求账本会显示“无法估算”，每日费用配额也不会累计该请求。</p>{error && <p className="form-error price-message">{error}</p>}{saved && <p className="form-success price-message">价格版本已保存，生效时间之后的新请求将使用它。</p>}</form><div className="table-wrap price-table"><table><thead><tr><th>模型 / 生效时间</th><th>缓存命中输入</th><th>缓存未命中输入</th><th>输出</th><th></th></tr></thead><tbody>{prices.map((rule) => <tr key={rule.id}><td><strong>{rule.model === "*" ? "全部模型（默认）" : rule.model}</strong><small>{rule.id === "price-default" ? "始终生效" : formatTime(rule.effective_at)} · <span className="mono">{rule.id}</span></small></td><td>¥ {rule.cache_hit_cny_per_million.toFixed(4)}</td><td>¥ {rule.cache_miss_cny_per_million.toFixed(4)}</td><td>¥ {rule.output_cny_per_million.toFixed(4)}</td><td><button className="icon-button small danger-icon" title="删除价格版本" disabled={deleting === rule.id} onClick={() => remove(rule)}><Trash2 size={15} /></button></td></tr>)}</tbody></table>{!prices.length && <Empty label="暂无价格版本，新请求费用将无法估算" />}</div></section>;
}

function Overview({ stats, accounts, usage }: { stats: Stats; accounts: Account[]; usage: RequestEvent[] }) {
  const hitRate = stats.prompt_tokens ? (stats.cache_hit_tokens / stats.prompt_tokens) * 100 : 0;
  const successRate = stats.requests ? (stats.successes / stats.requests) * 100 : 0;
  return <div className="stack">
    <section className="metric-grid">
      <Metric icon={Activity} label="总请求" value={formatNumber(stats.requests)} detail={`${successRate.toFixed(1)}% 成功`} tone="green" />
      <Metric icon={Gauge} label="Token 总量" value={formatCompact(stats.total_tokens)} detail={`输入 ${formatCompact(stats.prompt_tokens)} · 输出 ${formatCompact(stats.completion_tokens)}`} tone="blue" />
      <Metric icon={CircleDollarSign} label="已估费用" value={`¥ ${stats.estimated_cost_cny.toFixed(4)}`} detail={stats.unpriced_requests ? `${formatNumber(stats.unpriced_requests)} 条无法估算` : "全部已绑定价格版本"} tone="amber" />
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

function AlertsPage({ alerts, settings, onAcknowledge, onSilence, onResolve, onSaveSettings }: { alerts: Alert[]; settings: AlertSettings; onAcknowledge: (id: string) => Promise<void>; onSilence: (id: string) => Promise<void>; onResolve: (id: string) => Promise<void>; onSaveSettings: (settings: AlertSettings) => Promise<void> }) {
  const [filter, setFilter] = useState<"active" | "all">("active");
  const [draft, setDraft] = useState(settings);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  useEffect(() => setDraft(settings), [settings]);
  const active = alerts.filter((item) => item.status !== "resolved");
  const visible = filter === "active" ? active : alerts;
  const counts = {
    open: alerts.filter((item) => item.status === "open").length,
    acknowledged: alerts.filter((item) => item.status === "acknowledged").length,
    silenced: alerts.filter((item) => item.status === "silenced").length,
    resolved: alerts.filter((item) => item.status === "resolved").length,
  };
  const runAction = async (id: string, action: "acknowledge" | "silence" | "resolve") => {
    setBusy(`${id}:${action}`);
    setError("");
    try {
      if (action === "acknowledge") await onAcknowledge(id);
      if (action === "silence") await onSilence(id);
      if (action === "resolve") await onResolve(id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "处理告警失败");
    } finally {
      setBusy("");
    }
  };
  const updateNumber = (key: keyof AlertSettings, value: string) => setDraft((current) => ({ ...current, [key]: Number(value) }));
  return <div className="alert-stack">
    <section className="panel alert-panel">
      <PanelHead title="告警事件" subtitle={`${active.length} 个活动告警 · ${counts.open} 个未处理`} action={<div className="protocol-tabs"><button className={filter === "active" ? "active" : ""} onClick={() => setFilter("active")}>活动</button><button className={filter === "all" ? "active" : ""} onClick={() => setFilter("all")}>全部</button></div>} />
      <div className="alert-summary"><div><span>未处理</span><strong>{counts.open}</strong></div><div><span>已确认</span><strong>{counts.acknowledged}</strong></div><div><span>静默中</span><strong>{counts.silenced}</strong></div><div><span>已恢复</span><strong>{counts.resolved}</strong></div></div>
      {error && <div className="error-banner alert-error"><AlertCircle size={16} />{error}</div>}
      <div className="table-wrap alert-table"><table><thead><tr><th>级别</th><th>告警</th><th>范围</th><th>状态</th><th>时间</th><th>操作</th></tr></thead><tbody>{visible.map((item) => <tr key={item.id}><td><span className={`severity-pill ${item.severity}`}>{item.severity === "critical" ? "严重" : "警告"}</span></td><td className="alert-copy"><strong>{item.title}</strong><small>{item.message}</small></td><td><span>{alertScopeLabel(item.scope_type)}</span><small className="mono">{item.scope_id}</small></td><td><span className={`alert-state ${item.status}`}>{alertStatusLabel(item.status)}</span>{item.status === "silenced" && <small>至 {formatTime(item.silenced_until)}</small>}{item.status === "resolved" && <small>{formatTime(item.resolved_at)}</small>}</td><td><span>{formatTime(item.first_seen_at)}</span><small>最近 {formatTime(item.last_seen_at)}</small></td><td><div className="alert-actions">{item.status !== "resolved" && <><button className="secondary compact" title="确认告警" disabled={Boolean(busy) || item.status === "acknowledged"} onClick={() => runAction(item.id, "acknowledge")}><CheckCheck size={14} />确认</button><button className="secondary compact" title={`静默 ${settings.silence_minutes} 分钟`} disabled={Boolean(busy) || item.status === "silenced"} onClick={() => runAction(item.id, "silence")}><BellOff size={14} />静默</button><button className="secondary compact" title="标记为已恢复" disabled={Boolean(busy)} onClick={() => runAction(item.id, "resolve")}><Check size={14} />恢复</button></>}</div></td></tr>)}</tbody></table>{!visible.length && <Empty label={filter === "active" ? "当前没有活动告警" : "尚无告警记录"} />}</div>
    </section>
    <section className="panel alert-settings-panel">
      <PanelHead title="告警阈值" subtitle="账号余额、租户配额与近期错误率" />
      <form className="alert-settings-form" onSubmit={async (event) => { event.preventDefault(); setBusy("settings"); setError(""); setSaved(false); try { await onSaveSettings(draft); setSaved(true); } catch (err) { setError(err instanceof Error ? err.message : "保存告警设置失败"); } finally { setBusy(""); } }}>
        <label>余额下限（元）<input type="number" min="0" step="0.01" value={draft.balance_threshold_cny} onChange={(event) => updateNumber("balance_threshold_cny", event.target.value)} required /></label>
        <label>配额预警（%）<input type="number" min="1" max="99" step="1" value={draft.quota_warning_percent} onChange={(event) => updateNumber("quota_warning_percent", event.target.value)} required /></label>
        <label>错误率阈值（%）<input type="number" min="1" max="100" step="1" value={draft.error_rate_threshold_percent} onChange={(event) => updateNumber("error_rate_threshold_percent", event.target.value)} required /></label>
        <label>最少请求数<input type="number" min="1" max="10000" step="1" value={draft.error_rate_min_requests} onChange={(event) => updateNumber("error_rate_min_requests", event.target.value)} required /></label>
        <label>统计窗口（分钟）<input type="number" min="1" max="1440" step="1" value={draft.error_rate_window_minutes} onChange={(event) => updateNumber("error_rate_window_minutes", event.target.value)} required /></label>
        <label>默认静默（分钟）<input type="number" min="1" max="10080" step="1" value={draft.silence_minutes} onChange={(event) => updateNumber("silence_minutes", event.target.value)} required /></label>
        <div className="alert-settings-footer">{saved && <span className="form-success">告警阈值已保存。</span>}<button className="primary" disabled={busy === "settings"}>{busy === "settings" ? "正在保存" : "保存阈值"}</button></div>
      </form>
    </section>
  </div>;
}

function alertScopeLabel(scope: Alert["scope_type"]) { return scope === "account" ? "上游账号" : scope === "virtual_key" ? "租户密钥" : "平台流量"; }
function alertStatusLabel(status: Alert["status"]) { return status === "open" ? "未处理" : status === "acknowledged" ? "已确认" : status === "silenced" ? "静默中" : "已恢复"; }

function Accounts({ accounts, checkingAccount, onCreate, onEdit, onCheck, onTest, onToggle, onDelete }: { accounts: Account[]; checkingAccount: string; onCreate: () => void; onEdit: (account: Account) => void; onCheck: (account: Account) => Promise<void>; onTest: (account: Account) => void; onToggle: (account: Account) => Promise<void>; onDelete: (account: Account) => Promise<void> }) {
  return <div className="panel"><PanelHead title="账号池" subtitle={`${accounts.length} 个上游账号`} action={<button className="primary" onClick={onCreate}><Server size={16} />添加账号</button>} /><div className="table-wrap account-table"><table><thead><tr><th>账号</th><th>状态</th><th>余额</th><th>权重 / 活跃</th><th>来源</th><th></th></tr></thead><tbody>{accounts.map((a) => <tr key={a.id}><td><strong>{a.name}</strong><small>{a.id} · {a.api_key_prefix || "无 Key"}</small><small className="account-models" title={a.models?.join(", ") || "支持全部模型"}>支持模型：{a.models?.length ? a.models.join(", ") : "全部"}</small></td><td><Status ok={a.healthy} pending={a.check_status === "unchecked" || a.check_status === "disabled"} label={accountStatusLabel(a)} /></td><td>{a.balances?.length ? a.balances.map((b) => <div key={b.currency} className="money">{b.currency} {b.total_balance}</div>) : <span className="muted">暂无快照</span>}{a.balance_updated_at && <small>检测 {formatTime(a.balance_updated_at)}</small>}{a.balance_error && <small className="danger-text">{a.balance_error}</small>}</td><td>{a.weight} / {a.active}</td><td><span className={`source-tag ${a.managed ? "managed" : "env"}`}>{a.managed ? "控制台" : "环境变量"}</span></td><td><div className="row-actions"><button className="icon-button small" title="检测余额" disabled={!a.enabled || checkingAccount === a.id} onClick={() => onCheck(a)}><RefreshCw className={checkingAccount === a.id ? "spin" : ""} size={15} /></button><button className="icon-button small" title="测试 API" disabled={!a.enabled} onClick={() => onTest(a)}><FlaskConical size={15} /></button>{a.managed && <><button className="icon-button small" title="编辑账号" onClick={() => onEdit(a)}><Pencil size={15} /></button><label className="switch" title={a.enabled ? "停用账号" : "启用账号"}><input type="checkbox" checked={a.enabled} onChange={() => onToggle(a)} /><span /></label><button className="icon-button small danger-icon" title="删除账号" onClick={() => onDelete(a)}><Trash2 size={15} /></button></>}</div></td></tr>)}</tbody></table>{!accounts.length && <Empty label="尚未配置上游账号" />}</div></div>;
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

function AccountTestModal({ account, onClose, onRun, onSync }: { account: Account; onClose: () => void; onRun: (mode: "models" | "chat", model?: string) => Promise<AccountTestResult>; onSync: (models: string[]) => Promise<Account> }) {
  const [mode, setMode] = useState<"models" | "chat">("models");
  const [model, setModel] = useState(account.models?.[0] || "deepseek-chat");
  const [busy, setBusy] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [synced, setSynced] = useState(false);
  const [result, setResult] = useState<AccountTestResult | null>(null);
  const [error, setError] = useState("");
  const modeMeta = mode === "models"
    ? { title: "获取模型列表", endpoint: "GET /models", action: "检测模型列表" }
    : { title: "发送 Chat 请求", endpoint: "POST /chat/completions", action: "发送 Chat 测试" };
  const run = async () => {
    setBusy(true);
    setError("");
    setResult(null);
    setSynced(false);
    try {
      setResult(await onRun(mode, mode === "chat" ? model.trim() : undefined));
    } catch (err) {
      setError(err instanceof Error ? err.message : "API 测试失败");
    } finally {
      setBusy(false);
    }
  };
  const syncModels = async () => {
    if (!result?.models?.length) return;
    setSyncing(true);
    setError("");
    try {
      await onSync(result.models);
      setSynced(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "同步模型失败");
    } finally {
      setSyncing(false);
    }
  };
  return <div className="modal-backdrop"><div className="modal account-test-modal"><div className="modal-head"><div><h2>验证上游账号</h2><p>{account.name} · {account.base_url}</p></div><button className="icon-button" title="关闭" onClick={onClose}><X size={19} /></button></div><div className="account-test-body"><div className="account-test-summary"><div><span>当前检测</span><strong>{modeMeta.title}</strong></div><code>{modeMeta.endpoint}</code></div><div className="test-mode-row"><div className="mode-switch-head"><span>检测方式</span><div className="protocol-tabs"><button className={mode === "models" ? "active" : ""} aria-pressed={mode === "models"} onClick={() => { setMode("models"); setResult(null); setError(""); setSynced(false); }}>模型列表</button><button className={mode === "chat" ? "active" : ""} aria-pressed={mode === "chat"} onClick={() => { setMode("chat"); setResult(null); setError(""); setSynced(false); }}>Chat</button></div></div>{mode === "chat" && <label>请求模型<input value={model} onChange={(event) => setModel(event.target.value)} required /></label>}</div>{error && <p className="form-error">{error}</p>}{result && <div className={`account-test-result ${result.ok ? "ok" : "bad"}`}><div className="test-result-head">{result.ok ? <Check size={18} /> : <AlertCircle size={18} />}<div><strong>{result.ok ? "连接正常" : "请求失败"}</strong><span>{result.status ? `HTTP ${result.status} · ` : ""}{result.latency_ms} ms</span></div></div>{result.error && <p>{result.error}</p>}{result.mode === "models" && result.ok && <><p className="model-result-copy">返回模型（{result.models?.length || 0} 个）：{result.models?.slice(0, 12).join(", ") || "未返回模型 ID"}</p>{account.managed && Boolean(result.models?.length) && <><button className="secondary model-sync-button" disabled={syncing || synced} onClick={syncModels}>{synced ? <Check size={15} /> : <RefreshCw className={syncing ? "spin" : ""} size={15} />}{synced ? "已保存为支持模型" : syncing ? "正在保存" : "保存为支持模型"}</button>{synced && <p className="model-sync-state">已保存 {result.models?.length || 0} 个模型，可在编辑账号中查看。</p>}</>}</>}{result.mode === "chat" && result.ok && <p>{result.model || model}：{result.output || "请求成功，响应内容为空"}</p>}</div>}<div className="modal-actions"><button className="secondary" onClick={onClose}>关闭</button><button className="primary" disabled={busy || syncing || (mode === "chat" && !model.trim())} onClick={run}><FlaskConical size={15} />{busy ? "正在请求" : modeMeta.action}</button></div></div></div></div>;
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
  return <div className="modal-backdrop"><div className="modal"><div className="modal-head"><div><h2>{account ? "编辑上游账号" : "添加上游账号"}</h2><p>{account ? "修改账号参数，API Key 留空则保持不变。" : "添加一个可参与请求转发和余额轮询的账号。"}</p></div><button className="icon-button" title="关闭" onClick={onClose}><X size={19} /></button></div><form onSubmit={submit} className="create-form"><div className="form-grid">{!account && <label>账号 ID<input value={id} onChange={(e) => setID(e.target.value)} placeholder="acct-prod" /></label>}<label>名称<input value={name} onChange={(e) => setName(e.target.value)} required placeholder="生产主账号" /></label><label>API Key<input type="password" value={apiKey} onChange={(e) => setAPIKey(e.target.value)} placeholder={account ? "留空保持不变" : "sk-..."} required={!account} autoComplete="new-password" /></label><label>Base URL<input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} required placeholder="https://api.deepseek.com" /></label><label>权重<input type="number" min="1" max="1000" value={weight} onChange={(e) => setWeight(e.target.value)} /></label><label>支持模型<input value={models} onChange={(e) => setModels(e.target.value)} placeholder="留空表示全部模型" /></label></div><label className="check-row"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />启用账号</label>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>取消</button><button className="primary" disabled={busy}>{busy ? "正在保存" : "保存账号"}</button></div></form></div></div>;
}

function Usage({ events, summary, onApply, onExport }: { events: RequestEvent[]; summary: UsageSummary | null; onApply: (query: string) => void; onExport: (query: string) => Promise<void> }) {
  const [tenant, setTenant] = useState(""); const [model, setModel] = useState(""); const [start, setStart] = useState(""); const [end, setEnd] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const query = (limit = "200") => { const q = new URLSearchParams({ limit }); if (tenant) q.set("tenant_id", tenant); if (model) q.set("model", model); if (start) q.set("start", start); if (end) q.set("end", end); return q.toString(); };
  const submit = (e: FormEvent) => { e.preventDefault(); onApply(query()); };
  const exportCSV = async () => { setBusy(true); setError(""); try { await onExport(query("10000")); } catch (err) { setError(err instanceof Error ? err.message : "导出失败"); } finally { setBusy(false); } };
  const successRate = summary?.requests ? (summary.successes / summary.requests) * 100 : 0;
  const maxRequests = Math.max(...(summary?.daily ?? []).map((item) => item.requests), 1);
  return <div className="usage-stack"><section className="panel"><PanelHead title="用量汇总" subtitle={summary ? `${summary.start.slice(0, 10)} 至 ${inclusiveEndDate(summary.end)}` : "选择时间范围查看"} action={<button className="secondary" onClick={exportCSV} disabled={busy}><Download size={15} />{busy ? "正在导出" : "导出 CSV"}</button>} /><form className="usage-filters" onSubmit={submit}><label>开始日期<input type="date" value={start} onChange={(e) => setStart(e.target.value)} /></label><label>结束日期<input type="date" value={end} onChange={(e) => setEnd(e.target.value)} /></label><label>租户 ID<input value={tenant} onChange={(e) => setTenant(e.target.value)} placeholder="全部租户" /></label><label>模型<input value={model} onChange={(e) => setModel(e.target.value)} placeholder="全部模型" /></label><button className="primary">应用筛选</button></form>{error && <p className="form-error usage-error">{error}</p>}{summary && <><div className="summary-grid"><Metric icon={Activity} label="请求数" value={formatNumber(summary.requests)} detail={`${successRate.toFixed(1)}% 成功`} tone="green" /><Metric icon={Gauge} label="Token" value={formatCompact(summary.total_tokens)} detail={`输入 ${formatCompact(summary.prompt_tokens)} · 输出 ${formatCompact(summary.completion_tokens)}`} tone="blue" /><Metric icon={CircleDollarSign} label="已估费用" value={`¥ ${summary.estimated_cost_cny.toFixed(4)}`} detail={summary.unpriced_requests ? `${formatNumber(summary.unpriced_requests)} 条无法估算` : "全部已绑定价格版本"} tone="amber" /><Metric icon={AlertCircle} label="失败请求" value={formatNumber(summary.errors)} detail="需关注上游状态" tone="red" /></div><div className="usage-chart"><div className="chart-head"><strong>每日趋势</strong><span>请求数 / Token</span></div><div className="chart-bars">{summary.daily.length ? summary.daily.map((item) => <div className="chart-bar" key={item.date} title={`${item.date} · ${item.requests} 请求 · ${formatCompact(item.total_tokens)} Token${item.unpriced_requests ? ` · ${item.unpriced_requests} 条无法估算` : ""}`}><div className="bar-column" style={{ height: `${Math.max(6, (item.requests / maxRequests) * 100)}%` }} /><small>{item.date.slice(5)}</small></div>) : <Empty label="所选范围暂无请求" />}</div></div><div className="ranking-grid"><Ranking title="租户排行" items={summary.by_tenant} /><Ranking title="模型排行" items={summary.by_model} /></div></>}</section><section className="panel"><PanelHead title="请求事件" subtitle={`当前显示 ${events.length} 条记录`} /><RequestTable events={events} /></section></div>;
}

function Ranking({ title, items }: { title: string; items: UsageBreakdown[] }) { return <div className="ranking"><strong>{title}</strong>{items.length ? items.slice(0, 5).map((item) => <div key={item.id}><span title={item.id}>{item.id || "未标识"}</span><b>{formatCompact(item.total_tokens)}</b><small>¥ {item.estimated_cost_cny.toFixed(4)}{item.unpriced_requests ? ` · ${item.unpriced_requests} 条未估` : ""}</small></div>) : <Empty label="暂无数据" />}</div>; }

function RequestTable({ events, compact = false }: { events: RequestEvent[]; compact?: boolean }) {
  return <div className={`table-wrap request-table ${compact ? "compact" : ""}`}><table><thead><tr><th>时间 / 请求</th><th>租户</th><th>模型</th><th>状态</th><th>Token</th>{!compact && <><th>最终上游 / 尝试</th><th>首字节</th><th>费用</th></>}</tr></thead><tbody>{events.map((e) => <tr key={e.request_id}><td><span>{formatTime(e.created_at)}</span><small className="mono">{e.request_id.slice(0, 10)}</small></td><td>{e.tenant_id || "-"}</td><td>{e.model || "-"}</td><td><Status ok={e.status >= 200 && e.status < 400} label={String(e.status)} /></td><td>{formatNumber(e.usage.total_tokens)}</td>{!compact && <><td><span>{e.account_id || "-"}</span><small>{Math.max(e.attempts || 1, 1)} 次尝试</small></td><td>{e.first_byte_ms} ms</td><td><PriceValue event={e} /></td></>}</tr>)}</tbody></table>{!events.length && <Empty label="暂无请求记录" />}</div>;
}

function PriceValue({ event }: { event: RequestEvent }) {
  if (event.price_status === "estimated" || event.price_status === "legacy" || !event.price_status) {
    return <><span>¥ {event.estimated_cost_cny.toFixed(5)}</span><small title={event.price_rule_id || "历史全局配置"}>{event.price_rule_id || "历史配置"}</small></>;
  }
  return <span className="price-missing" title={event.price_status === "missing" ? "该模型在请求发生时间没有匹配的价格版本" : "上游响应没有返回 usage"}>{event.price_status === "missing" ? "缺少价格" : "无 usage"}</span>;
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
function recentUsageQuery() { const end = new Date(); const start = new Date(end); start.setUTCDate(start.getUTCDate() - 6); return `limit=100&start=${start.toISOString().slice(0, 10)}&end=${end.toISOString().slice(0, 10)}`; }
function inclusiveEndDate(value: string) { const end = new Date(value); end.setUTCDate(end.getUTCDate() - 1); return end.toISOString().slice(0, 10); }
function numberOrZero(value: string) { return value ? Number(value) : 0; }
function localDateTimeInput() { const value = new Date(); value.setMinutes(value.getMinutes() - value.getTimezoneOffset()); return value.toISOString().slice(0, 16); }
function quotaText(q: QuotaPolicy) { const parts = []; if (q.requests_per_minute) parts.push(`${q.requests_per_minute} RPM`); if (q.concurrent_requests) parts.push(`${q.concurrent_requests} 并发`); if (q.daily_tokens) parts.push(`${formatCompact(q.daily_tokens)} Token/日`); if (q.daily_cost_cny) parts.push(`¥${q.daily_cost_cny}/日`); return parts.join(" · ") || "不限"; }

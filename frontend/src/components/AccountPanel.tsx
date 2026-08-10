"use client";

import { apiPath } from "@/lib/base-path";
import { apiClient } from "@/lib/apiClient";
import { useCallback, useEffect, useLayoutEffect, useMemo, useState } from "react";
import { useAuth } from "@/lib/auth-context";
import { useLocale } from "@/lib/i18n";
import {
  User,
  Lock,
  Eye,
  EyeOff,
  Check,
  Loader2,
  KeyRound,
  Pencil,
  Plus,
  Copy,
  Trash2,
  X,
  ShieldCheck,
} from "lucide-react";
import {
  createAPIKey,
  listAPIKeys,
  revokeAllAPIKeys,
  revokeAPIKey,
  type APIKeyRecord,
} from "@/api/apiKeys";

export function AccountPanel() {
	const { user, refreshUser, passwordLoginEnabled, OIDCEnabled, oidcProviderName, oidcLinked } = useAuth();
	const { locale } = useLocale();
	const [oidcError, setOIDCError] = useState("");

	useLayoutEffect(() => {
		const target = new URL(window.location.href);
		if (!target.searchParams.has("oidc_error")) return;
		window.sessionStorage.removeItem(pendingAPIKeyIntentKey);
		setOIDCError(locale === "zh-CN" ? "单点登录未完成，请重试。" : "Single sign-on did not complete. Please try again.");
		target.searchParams.delete("oidc_error");
		window.history.replaceState({}, "", target.pathname + target.search + target.hash);
	}, [locale]);

  return (
    <div className="space-y-6">
	  {oidcError && <p className="rounded-xl border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400">{oidcError}</p>}
      {/* 用户信息概览 */}
      <div className="rounded-2xl border border-border/40 bg-gradient-to-br from-accent/5 via-card to-card p-5 sm:p-6">
        <div className="flex items-center gap-4">
          <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-accent/15 text-accent">
            <User className="h-7 w-7" />
          </div>
          <div>
            <h2 className="text-lg font-bold text-foreground">
              {user?.nickname || user?.username}
            </h2>
            <p className="text-sm text-muted">
              @{user?.username} · {user?.role === "admin" ? "管理员" : "普通用户"}
            </p>
          </div>
        </div>
      </div>

      {/* 修改昵称 */}
      <NicknameSection onSuccess={refreshUser} />

      {/* 修改密码 */}
      <PasswordSection />

	  {OIDCEnabled && (
		<OIDCIdentitySection
		  providerName={oidcProviderName}
		  linked={oidcLinked}
		  hasPassword={user?.hasPassword === true}
		  passwordLoginEnabled={passwordLoginEnabled}
		  onChanged={refreshUser}
		/>
	  )}

      {/* API 密钥 */}
      <APIKeySection />
    </div>
  );
}

function OIDCIdentitySection({ providerName, linked, hasPassword, passwordLoginEnabled, onChanged }: {
	providerName: string;
	linked: boolean;
	hasPassword: boolean;
	passwordLoginEnabled: boolean;
	onChanged: () => Promise<void>;
}) {
	const { locale } = useLocale();
	const [error, setError] = useState("");
	const [saving, setSaving] = useState(false);
	const [password, setPassword] = useState("");
	const isChinese = locale === "zh-CN";

	const startLink = async () => {
		setSaving(true);
		setError("");
		try {
			if (hasPassword) {
				await apiClient.post("/api/auth/reauth/password", { password });
			}
			const returnTo = window.location.pathname + window.location.search;
			const form = document.createElement("form");
			form.method = "POST";
			form.action = `${apiPath("/api/auth/oidc/link")}?returnTo=${encodeURIComponent(returnTo)}`;
			document.body.appendChild(form);
			form.submit();
		} catch (err) {
			setError(getAPIErrorMessage(err, isChinese ? "身份确认失败" : "Authentication failed"));
			setSaving(false);
		}
	};
	const unlink = async () => {
		setSaving(true);
		setError("");
		try {
			await apiClient.delete("/api/auth/oidc/link");
			await onChanged();
		} catch (err) {
			if (redirectForOIDCReauthentication(err)) return;
			setError(getAPIErrorMessage(err, isChinese ? "解除绑定失败" : "Failed to unlink identity"));
		} finally {
			setSaving(false);
		}
	};

	return (
		<section className="rounded-2xl border border-border/40 bg-card p-5">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<div>
					<h3 className="flex items-center gap-2 text-sm font-semibold text-foreground">
						<ShieldCheck className="h-4 w-4 text-accent" />
						{isChinese ? "外部身份" : "External identity"}
					</h3>
					<p className="mt-1 text-xs text-muted">
						{linked
							? (isChinese ? `已绑定 ${providerName}` : `Linked to ${providerName}`)
							: (isChinese ? `绑定 ${providerName} 后可使用单点登录` : `Link ${providerName} to use single sign-on`)}
					</p>
				</div>
				{linked ? (
					<button type="button" disabled={saving || !hasPassword || !passwordLoginEnabled} onClick={() => void unlink()} className="h-9 rounded-lg border border-red-500/30 px-3 text-xs text-red-400 hover:bg-red-500/10 disabled:opacity-50">
						{saving ? (isChinese ? "处理中" : "Working") : (isChinese ? "解除绑定" : "Unlink")}
					</button>
				) : (
					<div className="flex items-center gap-2">
						{hasPassword && <input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={isChinese ? "当前密码" : "Current password"} className="h-9 w-36 rounded-lg border border-border bg-background px-3 text-xs text-foreground focus:border-accent focus:outline-none" />}
						<button type="button" disabled={saving || (hasPassword && !password)} onClick={() => void startLink()} className="h-9 rounded-lg bg-accent px-3 text-xs font-medium text-white hover:bg-accent/90 disabled:opacity-50">
							{saving ? (isChinese ? "处理中" : "Working") : (isChinese ? "绑定身份" : "Link identity")}
						</button>
					</div>
				)}
			</div>
			{linked && (!hasPassword || !passwordLoginEnabled) && <p className="mt-3 text-xs text-amber-400">
				{!passwordLoginEnabled
					? (isChinese ? "密码登录关闭时不能解除最后一个外部身份。" : "The last external identity cannot be removed while password login is disabled.")
					: (isChinese ? "请先设置其他登录方式，再解除最后一个外部身份。" : "Add another login method before removing the last external identity.")}
			</p>}
			{error && <p className="mt-3 text-xs text-red-400">{error}</p>}
		</section>
	);
}

const apiKeyCopy = {
  "zh-CN": {
    title: "API 密钥",
    description: "用于脚本或客户端访问，权限始终跟随当前账户。",
    create: "创建密钥",
    revokeAll: "撤销全部",
    empty: "尚未创建 API 密钥",
    active: "有效",
    expired: "已过期",
    revoked: "已撤销",
    createdAt: "创建于",
    lastUsed: "最后使用",
    neverUsed: "从未使用",
    expires: "到期于",
    neverExpires: "永不过期",
    revoke: "撤销密钥",
    confirmRevoke: "确定撤销这个 API 密钥吗？撤销后无法恢复。",
    loadFailed: "加载 API 密钥失败",
    revokeFailed: "撤销 API 密钥失败",
    createTitle: "创建 API 密钥",
    name: "名称",
    namePlaceholder: "例如：家庭自动化",
    expiry: "有效期",
    days30: "30 天",
    days90: "90 天",
    days365: "1 年（推荐）",
    noExpiry: "永不过期",
    currentPassword: "当前密码",
    passwordPlaceholder: "用于确认本次操作",
    cancel: "取消",
    creating: "创建中",
    createFailed: "创建 API 密钥失败",
    createdTitle: "密钥已创建",
    createdWarning: "请立即保存。关闭后将无法再次查看完整密钥。",
    copy: "复制密钥",
    copied: "已复制",
    close: "关闭",
    revokeAllTitle: "撤销全部 API 密钥",
    revokeAllWarning: "所有使用这些密钥的脚本和客户端都会立即失去访问权限。",
    revokeAllConfirm: "确认全部撤销",
    revokeAllFailed: "撤销全部 API 密钥失败",
  },
  en: {
    title: "API Keys",
    description: "For scripts and clients. Access always follows this account's current permissions.",
    create: "Create key",
    revokeAll: "Revoke all",
    empty: "No API keys created",
    active: "Active",
    expired: "Expired",
    revoked: "Revoked",
    createdAt: "Created",
    lastUsed: "Last used",
    neverUsed: "Never used",
    expires: "Expires",
    neverExpires: "Never expires",
    revoke: "Revoke key",
    confirmRevoke: "Revoke this API key? This cannot be undone.",
    loadFailed: "Failed to load API keys",
    revokeFailed: "Failed to revoke API key",
    createTitle: "Create API key",
    name: "Name",
    namePlaceholder: "For example: Home automation",
    expiry: "Expiry",
    days30: "30 days",
    days90: "90 days",
    days365: "1 year (recommended)",
    noExpiry: "Never expires",
    currentPassword: "Current password",
    passwordPlaceholder: "Confirm this action",
    cancel: "Cancel",
    creating: "Creating",
    createFailed: "Failed to create API key",
    createdTitle: "API key created",
    createdWarning: "Save it now. The complete key cannot be shown again after closing.",
    copy: "Copy key",
    copied: "Copied",
    close: "Close",
    revokeAllTitle: "Revoke all API keys",
    revokeAllWarning: "Every script and client using these keys will immediately lose access.",
    revokeAllConfirm: "Revoke all keys",
    revokeAllFailed: "Failed to revoke all API keys",
  },
} as const;

type PendingAPIKeyIntent =
  | { type: "create"; name: string; expiresInDays: number }
  | { type: "revoke-all" };

const pendingAPIKeyIntentKey = "nowen-reader:oidc:api-key-intent";

function takePendingAPIKeyIntent(): PendingAPIKeyIntent | null {
  try {
	if (new URLSearchParams(window.location.search).has("oidc_error")) {
	  window.sessionStorage.removeItem(pendingAPIKeyIntentKey);
	  return null;
	}
    const raw = window.sessionStorage.getItem(pendingAPIKeyIntentKey);
    window.sessionStorage.removeItem(pendingAPIKeyIntentKey);
    if (!raw) return null;
    const value = JSON.parse(raw) as Partial<PendingAPIKeyIntent>;
    if (value.type === "revoke-all") return { type: "revoke-all" };
    if (value.type === "create" && typeof value.name === "string" && typeof value.expiresInDays === "number") {
      return { type: "create", name: value.name, expiresInDays: value.expiresInDays };
    }
  } catch {
    window.sessionStorage.removeItem(pendingAPIKeyIntentKey);
  }
  return null;
}

function APIKeySection() {
	const { user } = useAuth();
  const { locale } = useLocale();
  const text = apiKeyCopy[locale];
  const [keys, setKeys] = useState<APIKeyRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [showRevokeAll, setShowRevokeAll] = useState(false);
  const [createdKey, setCreatedKey] = useState("");
  const [copied, setCopied] = useState(false);

  const loadKeys = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setKeys(await listAPIKeys());
    } catch (err) {
      setError(getAPIErrorMessage(err, text.loadFailed));
    } finally {
      setLoading(false);
    }
  }, [text.loadFailed]);

  useEffect(() => {
    void loadKeys();
  }, [loadKeys]);

  useEffect(() => {
    const intent = takePendingAPIKeyIntent();
    if (!intent) return;
    void (async () => {
      setError("");
      try {
        if (intent.type === "create") {
          const response = await createAPIKey({ name: intent.name, expiresInDays: intent.expiresInDays });
          setCreatedKey(response.key);
          setCopied(false);
        } else {
          await revokeAllAPIKeys();
        }
        await loadKeys();
      } catch (err) {
        setError(getAPIErrorMessage(err, intent.type === "create" ? text.createFailed : text.revokeAllFailed));
      }
    })();
  }, [loadKeys, text.createFailed, text.revokeAllFailed]);

  const activeKeyCount = useMemo(() => {
    const now = Date.now();
    return keys.filter((key) => !key.revokedAt && (!key.expiresAt || Date.parse(key.expiresAt) > now)).length;
  }, [keys]);

  const handleRevoke = async (key: APIKeyRecord) => {
    if (!window.confirm(text.confirmRevoke)) return;
    setError("");
    try {
      await revokeAPIKey(key.id);
      await loadKeys();
    } catch (err) {
      setError(getAPIErrorMessage(err, text.revokeFailed));
    }
  };

  const formatDate = (value: string) =>
    new Intl.DateTimeFormat(locale === "zh-CN" ? "zh-CN" : "en", {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    }).format(new Date(value));

  const getStatus = (key: APIKeyRecord) => {
    if (key.revokedAt) return { label: text.revoked, className: "bg-red-500/10 text-red-400" };
    if (key.expiresAt && Date.parse(key.expiresAt) <= Date.now()) {
      return { label: text.expired, className: "bg-amber-500/10 text-amber-400" };
    }
    return { label: text.active, className: "bg-green-500/10 text-green-400" };
  };

  return (
    <section className="overflow-hidden rounded-2xl border border-border/40 bg-card">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/30 px-5 py-4">
        <div>
          <h3 className="flex items-center gap-2 text-sm font-semibold text-foreground">
            <ShieldCheck className="h-4 w-4 text-accent" />
            {text.title}
          </h3>
          <p className="mt-1 text-xs text-muted">{text.description}</p>
        </div>
        <div className="flex items-center gap-2">
          {activeKeyCount > 0 && (
            <button
              type="button"
              onClick={() => setShowRevokeAll(true)}
              className="inline-flex h-9 items-center gap-2 rounded-lg border border-red-500/30 px-3 text-xs font-medium text-red-400 transition-colors hover:bg-red-500/10"
            >
              <Trash2 className="h-4 w-4" />
              {text.revokeAll}
            </button>
          )}
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="inline-flex h-9 items-center gap-2 rounded-lg bg-accent px-3 text-xs font-medium text-white transition-colors hover:bg-accent/90"
          >
            <Plus className="h-4 w-4" />
            {text.create}
          </button>
        </div>
      </div>

      {error && <div className="mx-5 mt-4 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-400">{error}</div>}

      <div className="divide-y divide-border/30">
        {loading ? (
          <div className="flex h-28 items-center justify-center text-muted">
            <Loader2 className="h-5 w-5 animate-spin" />
          </div>
        ) : keys.length === 0 ? (
          <div className="px-5 py-10 text-center text-sm text-muted">{text.empty}</div>
        ) : (
          keys.map((key) => {
            const status = getStatus(key);
            const canRevoke = !key.revokedAt && (!key.expiresAt || Date.parse(key.expiresAt) > Date.now());
            return (
              <div key={key.id} className="flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-sm font-medium text-foreground">{key.name}</span>
                    <span className={`rounded px-2 py-0.5 text-[11px] font-medium ${status.className}`}>{status.label}</span>
                  </div>
                  <code className="mt-1 block text-xs text-muted">{key.keyPrefix}</code>
                  <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted">
                    <span>{text.createdAt}: {formatDate(key.createdAt)}</span>
                    <span>{text.lastUsed}: {key.lastUsedAt ? formatDate(key.lastUsedAt) : text.neverUsed}</span>
                    <span>{text.expires}: {key.expiresAt ? formatDate(key.expiresAt) : text.neverExpires}</span>
                  </div>
                </div>
                {canRevoke && (
                  <button
                    type="button"
                    onClick={() => void handleRevoke(key)}
                    title={text.revoke}
                    aria-label={text.revoke}
                    className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-muted transition-colors hover:bg-red-500/10 hover:text-red-400"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                )}
              </div>
            );
          })
        )}
      </div>

      {showCreate && (
        <CreateAPIKeyDialog
          text={text}
		  hasPassword={user?.hasPassword !== false}
          onClose={() => setShowCreate(false)}
          onCreated={async (plaintext) => {
            setShowCreate(false);
            setCreatedKey(plaintext);
            setCopied(false);
            await loadKeys();
          }}
        />
      )}

      {showRevokeAll && (
        <RevokeAllAPIKeysDialog
          text={text}
		  hasPassword={user?.hasPassword !== false}
          onClose={() => setShowRevokeAll(false)}
          onRevoked={async () => {
            setShowRevokeAll(false);
            await loadKeys();
          }}
        />
      )}

      {createdKey && (
        <DialogShell title={text.createdTitle} onClose={() => setCreatedKey("")} closeLabel={text.close}>
          <p className="text-sm text-amber-400">{text.createdWarning}</p>
          <div className="mt-4 break-all rounded-lg border border-border bg-background p-3 font-mono text-xs text-foreground">
            {createdKey}
          </div>
          <div className="mt-5 flex justify-end gap-2">
            <button
              type="button"
              onClick={async () => {
                try {
                  await copyText(createdKey);
                  setCopied(true);
                } catch {
                  setCopied(false);
                }
              }}
              className="inline-flex h-9 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-white hover:bg-accent/90"
            >
              {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
              {copied ? text.copied : text.copy}
            </button>
            <button type="button" onClick={() => setCreatedKey("")} className="h-9 rounded-lg border border-border px-4 text-sm text-foreground hover:bg-foreground/5">
              {text.close}
            </button>
          </div>
        </DialogShell>
      )}
    </section>
  );
}

type APIKeyText = (typeof apiKeyCopy)["zh-CN"] | (typeof apiKeyCopy)["en"];

function CreateAPIKeyDialog({ text, hasPassword, onClose, onCreated }: {
  text: APIKeyText;
	hasPassword: boolean;
  onClose: () => void;
  onCreated: (plaintext: string) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [expiresInDays, setExpiresInDays] = useState(365);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
	  const response = await createAPIKey({
		name: name.trim(),
		currentPassword: hasPassword ? password : undefined,
		expiresInDays,
	  });
      await onCreated(response.key);
    } catch (err) {
	  if (redirectForOIDCReauthentication(err, { type: "create", name: name.trim(), expiresInDays })) return;
      setError(getAPIErrorMessage(err, text.createFailed));
    } finally {
      setSaving(false);
    }
  };

  return (
    <DialogShell title={text.createTitle} onClose={onClose} closeLabel={text.cancel}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <label className="block text-xs font-medium text-muted">
          {text.name}
          <input required maxLength={64} value={name} onChange={(event) => setName(event.target.value)} placeholder={text.namePlaceholder} className="mt-1.5 w-full rounded-lg border border-border bg-background px-3 py-2.5 text-sm text-foreground focus:border-accent focus:outline-none" />
        </label>
        <label className="block text-xs font-medium text-muted">
          {text.expiry}
          <select value={expiresInDays} onChange={(event) => setExpiresInDays(Number(event.target.value))} className="mt-1.5 w-full rounded-lg border border-border bg-background px-3 py-2.5 text-sm text-foreground focus:border-accent focus:outline-none">
            <option value={30}>{text.days30}</option>
            <option value={90}>{text.days90}</option>
            <option value={365}>{text.days365}</option>
            <option value={0}>{text.noExpiry}</option>
          </select>
        </label>
		{hasPassword && <label className="block text-xs font-medium text-muted">
          {text.currentPassword}
          <input required type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={text.passwordPlaceholder} className="mt-1.5 w-full rounded-lg border border-border bg-background px-3 py-2.5 text-sm text-foreground focus:border-accent focus:outline-none" />
		</label>}
        {error && <div className="rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-400">{error}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <button type="button" onClick={onClose} className="h-9 rounded-lg border border-border px-4 text-sm text-foreground hover:bg-foreground/5">{text.cancel}</button>
		  <button type="submit" disabled={saving || !name.trim() || (hasPassword && !password)} className="inline-flex h-9 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-white hover:bg-accent/90 disabled:opacity-50">
            {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
            {saving ? text.creating : text.create}
          </button>
        </div>
      </form>
    </DialogShell>
  );
}

function RevokeAllAPIKeysDialog({ text, hasPassword, onClose, onRevoked }: {
  text: APIKeyText;
	hasPassword: boolean;
  onClose: () => void;
  onRevoked: () => Promise<void>;
}) {
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
	  await revokeAllAPIKeys(hasPassword ? password : undefined);
      await onRevoked();
    } catch (err) {
	  if (redirectForOIDCReauthentication(err, { type: "revoke-all" })) return;
      setError(getAPIErrorMessage(err, text.revokeAllFailed));
    } finally {
      setSaving(false);
    }
  };

  return (
    <DialogShell title={text.revokeAllTitle} onClose={onClose} closeLabel={text.cancel}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <p className="text-sm text-red-400">{text.revokeAllWarning}</p>
		{hasPassword && <label className="block text-xs font-medium text-muted">
          {text.currentPassword}
          <input required type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={text.passwordPlaceholder} className="mt-1.5 w-full rounded-lg border border-border bg-background px-3 py-2.5 text-sm text-foreground focus:border-accent focus:outline-none" />
		</label>}
        {error && <div className="rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-400">{error}</div>}
        <div className="flex justify-end gap-2">
          <button type="button" onClick={onClose} className="h-9 rounded-lg border border-border px-4 text-sm text-foreground hover:bg-foreground/5">{text.cancel}</button>
		  <button type="submit" disabled={saving || (hasPassword && !password)} className="inline-flex h-9 items-center gap-2 rounded-lg bg-red-500 px-4 text-sm font-medium text-white hover:bg-red-500/90 disabled:opacity-50">
            {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
            {text.revokeAllConfirm}
          </button>
        </div>
      </form>
    </DialogShell>
  );
}

function DialogShell({ title, onClose, closeLabel, children }: {
  title: string;
  onClose: () => void;
  closeLabel: string;
  children: React.ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="dialog" aria-modal="true" aria-label={title}>
      <div className="w-full max-w-lg rounded-xl border border-border bg-card shadow-2xl">
        <div className="flex items-center justify-between border-b border-border/40 px-5 py-4">
          <h4 className="text-base font-semibold text-foreground">{title}</h4>
          <button type="button" onClick={onClose} title={closeLabel} aria-label={closeLabel} className="flex h-8 w-8 items-center justify-center rounded-lg text-muted hover:bg-foreground/5 hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="p-5">{children}</div>
      </div>
    </div>
  );
}

function getAPIErrorMessage(error: unknown, fallback: string): string {
  if (typeof error === "object" && error !== null && "message" in error && typeof error.message === "string") {
    return error.message;
  }
  return fallback;
}

function redirectForOIDCReauthentication(error: unknown, intent?: PendingAPIKeyIntent): boolean {
	if (typeof error !== "object" || error === null || !("code" in error) || error.code !== "reauth_required") {
		return false;
	}
	if (intent) {
		try {
			window.sessionStorage.setItem(pendingAPIKeyIntentKey, JSON.stringify(intent));
		} catch {
			// Reauthentication can still proceed if browser storage is unavailable.
		}
	}
	const returnTo = window.location.pathname + window.location.search;
	window.location.assign(`${apiPath("/api/auth/oidc/reauth")}?returnTo=${encodeURIComponent(returnTo)}`);
	return true;
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // Fall back for LAN deployments served over HTTP.
    }
  }

  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Copy failed");
}

/* ── 修改昵称区域 ── */
function NicknameSection({ onSuccess }: { onSuccess: () => Promise<void> }) {
  const { user } = useAuth();
  const [nickname, setNickname] = useState(user?.nickname || "");
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: "success" | "error"; text: string } | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setMessage(null);

    if (!nickname.trim()) {
      setMessage({ type: "error", text: "昵称不能为空" });
      return;
    }

    if (nickname === user?.nickname) {
      setMessage({ type: "error", text: "昵称未发生变化" });
      return;
    }

    setSaving(true);
    try {
      const res = await fetch(apiPath("/api/auth/users"), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          action: "updateProfile",
          nickname: nickname.trim(),
        }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || "修改失败");
      setMessage({ type: "success", text: "昵称修改成功" });
      await onSuccess();
    } catch (err) {
      setMessage({ type: "error", text: err instanceof Error ? err.message : "修改失败" });
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="rounded-2xl border border-border/40 bg-card overflow-hidden">
      <div className="px-5 py-4 border-b border-border/30">
        <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
          <Pencil className="h-4 w-4 text-accent" />
          修改昵称
        </h3>
      </div>
      <form onSubmit={handleSubmit} className="p-5 space-y-4">
        <div>
          <label className="block text-xs font-medium text-muted mb-1.5">昵称</label>
          <div className="relative">
            <User className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" />
            <input
              type="text"
              value={nickname}
              onChange={(e) => {
                setNickname(e.target.value);
                setMessage(null);
              }}
              placeholder="输入新昵称"
              className="w-full pl-10 pr-4 py-2.5 bg-background border border-border rounded-lg text-sm text-foreground placeholder:text-muted/50 focus:outline-none focus:border-accent transition-colors"
              maxLength={32}
            />
          </div>
        </div>

        {message && (
          <div className={`text-xs px-3 py-2 rounded-lg ${
            message.type === "success"
              ? "bg-green-500/10 text-green-400"
              : "bg-red-500/10 text-red-400"
          }`}>
            {message.text}
          </div>
        )}

        <button
          type="submit"
          disabled={saving}
          className="flex items-center gap-2 rounded-lg bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent/90 disabled:opacity-50 transition-colors"
        >
          {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}
          保存昵称
        </button>
      </form>
    </section>
  );
}

/* ── 修改密码区域 ── */
function PasswordSection() {
	const { user, refreshUser, passwordLoginEnabled } = useAuth();
  const hasPassword = user?.hasPassword !== false;
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [showOld, setShowOld] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [showConfirm, setShowConfirm] = useState(false);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: "success" | "error"; text: string } | null>(null);

  const resetForm = () => {
    setOldPassword("");
    setNewPassword("");
    setConfirmPassword("");
    setShowOld(false);
    setShowNew(false);
    setShowConfirm(false);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setMessage(null);

    if ((hasPassword && !oldPassword) || !newPassword || !confirmPassword) {
      setMessage({ type: "error", text: "请填写所有密码字段" });
      return;
    }

    if (newPassword.length < 6) {
      setMessage({ type: "error", text: "新密码至少需要 6 个字符" });
      return;
    }

    if (newPassword !== confirmPassword) {
      setMessage({ type: "error", text: "两次输入的新密码不一致" });
      return;
    }

    if (hasPassword && oldPassword === newPassword) {
      setMessage({ type: "error", text: "新密码不能与旧密码相同" });
      return;
    }

    setSaving(true);
    try {
	  if (hasPassword) {
		const res = await fetch(apiPath("/api/auth/users"), {
		  method: "PUT",
		  headers: { "Content-Type": "application/json" },
		  body: JSON.stringify({ action: "changePassword", oldPassword, newPassword }),
		});
		const data = await res.json();
		if (!res.ok) throw new Error(data.error || "修改密码失败");
	  } else {
		await apiClient.post("/api/auth/password", { newPassword });
		await refreshUser();
	  }
      setMessage({ type: "success", text: hasPassword ? "密码修改成功" : "本地密码设置成功" });
      resetForm();
    } catch (err) {
	  if (!hasPassword && redirectForOIDCReauthentication(err)) return;
      setMessage({ type: "error", text: err instanceof Error ? err.message : "修改密码失败" });
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="rounded-2xl border border-border/40 bg-card overflow-hidden">
      <div className="px-5 py-4 border-b border-border/30">
        <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
          <KeyRound className="h-4 w-4 text-accent" />
		  {hasPassword ? "修改密码" : "设置本地密码"}
        </h3>
      </div>
      <form onSubmit={handleSubmit} className="p-5 space-y-4">
		{!hasPassword && <p className="text-xs text-muted">
		  {passwordLoginEnabled
			? "设置一个本地应急密码后，即使身份提供方不可用也能登录。"
			: "本地密码会被安全保留；管理员重新启用密码登录后可用于应急恢复。"}
		</p>}
        {/* 当前密码 */}
        {hasPassword && <div>
          <label className="block text-xs font-medium text-muted mb-1.5">当前密码</label>
          <div className="relative">
            <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" />
            <input
              type={showOld ? "text" : "password"}
              value={oldPassword}
              onChange={(e) => {
                setOldPassword(e.target.value);
                setMessage(null);
              }}
              placeholder="输入当前密码"
              aria-label="当前密码"
              className="w-full pl-10 pr-12 py-2.5 bg-background border border-border rounded-lg text-sm text-foreground placeholder:text-muted/50 focus:outline-none focus:border-accent transition-colors"
            />
            <button
              type="button"
              onClick={() => setShowOld(!showOld)}
              aria-label={showOld ? "隐藏当前密码" : "显示当前密码"}
              className="absolute right-0 top-1/2 flex h-10 w-10 -translate-y-1/2 items-center justify-center text-muted transition-colors hover:text-foreground"
            >
              {showOld ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </button>
          </div>
        </div>}

        {/* 新密码 */}
        <div>
          <label className="block text-xs font-medium text-muted mb-1.5">新密码</label>
          <div className="relative">
            <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" />
            <input
              type={showNew ? "text" : "password"}
              value={newPassword}
              onChange={(e) => {
                setNewPassword(e.target.value);
                setMessage(null);
              }}
              placeholder="输入新密码（至少 6 个字符）"
              aria-label="新密码"
              className="w-full pl-10 pr-12 py-2.5 bg-background border border-border rounded-lg text-sm text-foreground placeholder:text-muted/50 focus:outline-none focus:border-accent transition-colors"
              minLength={6}
            />
            <button
              type="button"
              onClick={() => setShowNew(!showNew)}
              aria-label={showNew ? "隐藏新密码" : "显示新密码"}
              className="absolute right-0 top-1/2 flex h-10 w-10 -translate-y-1/2 items-center justify-center text-muted transition-colors hover:text-foreground"
            >
              {showNew ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </button>
          </div>
        </div>

        {/* 确认新密码 */}
        <div>
          <label className="block text-xs font-medium text-muted mb-1.5">确认新密码</label>
          <div className="relative">
            <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" />
            <input
              type={showConfirm ? "text" : "password"}
              value={confirmPassword}
              onChange={(e) => {
                setConfirmPassword(e.target.value);
                setMessage(null);
              }}
              placeholder="再次输入新密码"
              aria-label="确认新密码"
              className="w-full pl-10 pr-12 py-2.5 bg-background border border-border rounded-lg text-sm text-foreground placeholder:text-muted/50 focus:outline-none focus:border-accent transition-colors"
              minLength={6}
            />
            <button
              type="button"
              onClick={() => setShowConfirm(!showConfirm)}
              aria-label={showConfirm ? "隐藏确认密码" : "显示确认密码"}
              className="absolute right-0 top-1/2 flex h-10 w-10 -translate-y-1/2 items-center justify-center text-muted transition-colors hover:text-foreground"
            >
              {showConfirm ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </button>
          </div>
        </div>

        {message && (
          <div className={`text-xs px-3 py-2 rounded-lg ${
            message.type === "success"
              ? "bg-green-500/10 text-green-400"
              : "bg-red-500/10 text-red-400"
          }`}>
            {message.text}
          </div>
        )}

        <button
          type="submit"
          disabled={saving}
          className="flex items-center gap-2 rounded-lg bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent/90 disabled:opacity-50 transition-colors"
        >
          {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}
		  {hasPassword ? "修改密码" : "设置密码"}
        </button>
      </form>
    </section>
  );
}

export default AccountPanel;

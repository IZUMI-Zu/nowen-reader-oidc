"use client";

import { useCallback, useEffect, useState } from "react";
import {
  AlertTriangle,
  Check,
  Clipboard,
  Eye,
  EyeOff,
  KeyRound,
  Loader2,
  LockKeyhole,
  Play,
  RefreshCw,
  Save,
  ShieldCheck,
} from "lucide-react";
import {
  oidcAdminAPI,
  type OIDCAdminConfig,
} from "@/api/oidc";
import { apiPath } from "@/lib/base-path";
import { apiClient, type ApiError } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth-context";
import { useLocale } from "@/lib/i18n";
import {
  consumeOIDCResumeState,
  oidcReauthenticationReturnTo,
  storeOIDCResumeState,
  toOIDCAdminFields,
  toOIDCAdminForm,
  type OIDCAdminForm,
  type OIDCResumeState,
  type OIDCSensitiveAction,
} from "@/lib/oidc-admin-state";

const copy = {
  "zh-CN": {
    title: "OpenID Connect",
    description: "由管理员配置统一登录。保存草稿后，先完成连通性检查和真实测试登录，再启用。",
    loading: "正在加载 OIDC 配置…",
    loadFailed: "加载 OIDC 配置失败",
    environmentTitle: "当前由环境变量管理",
    environmentDescription: "Web 后台为只读，避免环境变量与数据库配置混用。请在部署环境中修改后重启服务。",
    recoveryTitle: "防止管理员被锁在系统外",
    recoveryDescription: "启用前必须用当前管理员完成测试登录。停用 OIDC 或关闭密码登录前，当前管理员还必须保留本地恢复密码。",
    forcePassword: "部署侧恢复开关已启用：密码登录会保持开放，Web 设置不会关闭该入口。",
    recoveryCommand: "紧急恢复：在部署环境设置 OIDC_FORCE_PASSWORD_LOGIN=true 并重启服务。",
    keyAndLogs: "备份：OIDC_CONFIG_KEY_FILE，或默认 {DATA_DIR}/secrets/oidc-config.key。诊断信息位于服务日志和 OIDC 配置审计中，且不记录密钥、Token 或授权码。",
    issuerURL: "Issuer URL",
    clientID: "Client ID",
    clientSecret: "Client Secret",
    providerName: "登录按钮名称",
    publicURL: "站点公开地址",
    callbackURL: "回调地址",
    basePath: "部署路径（BASE_PATH）",
    scopes: "Scopes",
    sessionTTL: "OIDC 会话最长时长（小时）",
    enabled: "启用 OIDC 登录",
    autoProvision: "允许首次登录自动创建普通用户",
    disablePassword: "关闭用户名密码登录",
    secretConfigured: "已保存密钥；留空表示不修改",
    secretMissing: "尚未保存密钥",
    secretPlaceholder: "输入新的 Client Secret",
    clearSecret: "保存时清除已存密钥",
    showSecret: "显示密钥",
    hideSecret: "隐藏密钥",
    copyCallback: "复制回调地址",
    copied: "已复制",
    save: "保存配置",
    saving: "保存中…",
    probe: "检查 Discovery",
    probing: "检查中…",
    test: "进行真实测试登录",
    testing: "正在跳转…",
    reload: "重新加载",
    saved: "配置已保存。协议字段发生变化后，需要重新完成测试登录。",
    probeOK: "Discovery 与端点检查成功；Client ID 和 Secret 仍需通过真实测试登录验证。",
    testHint: "测试登录使用已保存的草稿，并会把返回的 OIDC 身份绑定到当前管理员。",
    reauthTitle: "请确认管理员身份",
    reauthDescription: "这项操作会修改或验证登录配置，请输入当前密码后继续。",
    currentPassword: "当前密码",
    confirm: "确认并继续",
    cancel: "取消",
    status: "状态",
    lastVerified: "上次测试通过",
    never: "尚未通过",
    protectionLocal: "密钥使用本机文件保护",
    protectionExternal: "密钥使用外部挂载文件保护",
    errorFallback: "OIDC 操作失败",
    conflict: "配置已被其他管理员修改，请重新加载后再保存。",
    testRequired: "启用前必须先保存草稿并完成真实测试登录。",
    adminIdentityRequired: "启用前必须通过测试登录绑定当前管理员身份。",
    recoveryPasswordRequired: "停用 OIDC 或关闭密码登录前，当前管理员必须先设置本地恢复密码。",
    ttlInvalid: "OIDC 会话最长时长必须在 5 分钟到 720 小时之间。",
    disableImpact: (count: number) => `停用 OIDC 后，${count} 个没有本地密码的用户将无法重新登录。`,
    confirmDisableImpact: "我确认这些用户会受到影响，并仍要停用 OIDC",
    disableConfirmationRequired: "请先确认停用 OIDC 对无本地密码用户的影响。",
    disablePasswordWarning: "关闭用户名密码登录后，所有用户只能通过 OIDC 登录；紧急恢复需要部署侧开关。",
    confirmDisablePassword: "我确认关闭用户名密码登录，并已保存紧急恢复方式",
    disablePasswordConfirmationRequired: "请明确确认关闭用户名密码登录。",
    configurationInvalid: "OIDC 配置无效，请检查必填项、HTTPS 地址、scopes 和会话时长。",
    probeFailed: "无法验证 Provider Discovery，请检查地址、TLS、DNS 和网络连通性。",
    formRestored: "管理员验证已完成，未保存的表单已恢复并继续执行操作。",
    secretReentry: "未保存的表单已恢复。为避免在浏览器存储中保留 Client Secret，请重新输入密钥后再执行操作。",
    resumeStale: "OIDC 配置已被修改，未自动恢复之前的操作；请核对当前配置。",
    reauthFailed: "OIDC 管理员验证失败，请重试。",
    identityMismatch: "返回的 OIDC 身份不属于当前管理员。",
    configurationChanged: "验证期间 OIDC 配置已发生变化，请重新加载后重试。",
  },
  en: {
    title: "OpenID Connect",
    description: "Administrator-managed single sign-on. Save a draft, check discovery, then complete a real test login before enabling it.",
    loading: "Loading OIDC configuration…",
    loadFailed: "Failed to load OIDC configuration",
    environmentTitle: "Managed by environment variables",
    environmentDescription: "The Web form is read-only so environment and database settings cannot be mixed. Change the deployment environment and restart the service.",
    recoveryTitle: "Administrator lockout protection",
    recoveryDescription: "The current administrator must complete a test login before activation. A local recovery password is also required before OIDC or password login can be disabled.",
    forcePassword: "The deployment recovery switch is active. Password login remains available regardless of the Web setting.",
    recoveryCommand: "Emergency recovery: set OIDC_FORCE_PASSWORD_LOGIN=true in the deployment environment and restart the service.",
    keyAndLogs: "Backup: OIDC_CONFIG_KEY_FILE, or the default {DATA_DIR}/secrets/oidc-config.key. Diagnostics are in server logs and the OIDC configuration audit; secrets, tokens, and authorization codes are excluded.",
    issuerURL: "Issuer URL",
    clientID: "Client ID",
    clientSecret: "Client Secret",
    providerName: "Login button label",
    publicURL: "Public site URL",
    callbackURL: "Callback URL",
    basePath: "Deployment path (BASE_PATH)",
    scopes: "Scopes",
    sessionTTL: "Maximum OIDC session (hours)",
    enabled: "Enable OIDC login",
    autoProvision: "Create regular users on first successful login",
    disablePassword: "Disable username and password login",
    secretConfigured: "A secret is stored; leave blank to keep it",
    secretMissing: "No client secret is stored",
    secretPlaceholder: "Enter a new Client Secret",
    clearSecret: "Clear the stored secret when saving",
    showSecret: "Show secret",
    hideSecret: "Hide secret",
    copyCallback: "Copy callback URL",
    copied: "Copied",
    save: "Save configuration",
    saving: "Saving…",
    probe: "Check discovery",
    probing: "Checking…",
    test: "Run real test login",
    testing: "Redirecting…",
    reload: "Reload",
    saved: "Configuration saved. Protocol changes require another successful test login.",
    probeOK: "Discovery and endpoints are valid. Client ID and secret still require a real test login.",
    testHint: "The test uses the saved draft and binds the returned OIDC identity to the current administrator.",
    reauthTitle: "Confirm administrator identity",
    reauthDescription: "This action changes or validates sign-in configuration. Enter your current password to continue.",
    currentPassword: "Current password",
    confirm: "Confirm and continue",
    cancel: "Cancel",
    status: "Status",
    lastVerified: "Last verified",
    never: "Never",
    protectionLocal: "Secret protected by a local key file",
    protectionExternal: "Secret protected by an externally mounted key",
    errorFallback: "OIDC operation failed",
    conflict: "Another administrator changed this configuration. Reload before saving.",
    testRequired: "Save the draft and complete a real test login before enabling OIDC.",
    adminIdentityRequired: "Link the current administrator through a test login before enabling OIDC.",
    recoveryPasswordRequired: "Set a local administrator recovery password before disabling OIDC or password login.",
    ttlInvalid: "The maximum OIDC session must be between 5 minutes and 720 hours.",
    disableImpact: (count: number) => `Disabling OIDC will prevent ${count} user(s) without a local password from signing in again.`,
    confirmDisableImpact: "I understand the impact and still want to disable OIDC",
    disableConfirmationRequired: "Confirm the impact on users without local passwords before disabling OIDC.",
    disablePasswordWarning: "After password login is disabled, every user must use OIDC. Emergency recovery requires the deployment-side switch.",
    confirmDisablePassword: "I confirm password login will be disabled and the emergency recovery procedure is saved",
    disablePasswordConfirmationRequired: "Explicitly confirm that password login will be disabled.",
    configurationInvalid: "The OIDC configuration is invalid. Check required fields, HTTPS URLs, scopes, and session duration.",
    probeFailed: "Provider Discovery could not be validated. Check the URL, TLS, DNS, and network connectivity.",
    formRestored: "Administrator verification completed. The unsaved form was restored and the action resumed.",
    secretReentry: "The unsaved form was restored. Re-enter the Client Secret before retrying because secrets are never written to browser storage.",
    resumeStale: "The OIDC configuration changed, so the previous action was not resumed. Review the current configuration.",
    reauthFailed: "OIDC administrator verification failed. Try again.",
    identityMismatch: "The returned OIDC identity does not belong to the current administrator.",
    configurationChanged: "The OIDC configuration changed during verification. Reload and try again.",
  },
} as const;

const statusLabels = {
  "zh-CN": {
    disabled: "未启用",
    draft: "草稿",
    ready: "测试通过，待启用",
    active: "已启用",
    invalid: "配置无效",
    "environment-managed": "环境变量管理",
  },
  en: {
    disabled: "Disabled",
    draft: "Draft",
    ready: "Verified, ready to enable",
    active: "Active",
    invalid: "Invalid",
    "environment-managed": "Environment-managed",
  },
} as const;

function isReauthenticationRequired(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && error.code === "reauth_required";
}

function errorMessage(error: unknown, fallback: string): string {
  const candidate = error as Partial<ApiError> | null;
  return candidate && typeof candidate.message === "string" ? candidate.message : fallback;
}

export function OIDCSettingsPanel() {
  const { locale } = useLocale();
  const { user } = useAuth();
  const text = copy[locale];
  const [serverConfig, setServerConfig] = useState<OIDCAdminConfig | null>(null);
  const [form, setForm] = useState<OIDCAdminForm | null>(null);
  const [clientSecret, setClientSecret] = useState("");
  const [clearSecret, setClearSecret] = useState(false);
  const [showSecret, setShowSecret] = useState(false);
  const [busyAction, setBusyAction] = useState<OIDCSensitiveAction | "load" | null>("load");
  const [pendingAction, setPendingAction] = useState<OIDCSensitiveAction | null>(null);
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState<{ kind: "success" | "error"; text: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirmOIDCOnlyUsers, setConfirmOIDCOnlyUsers] = useState(false);
  const [confirmDisablePasswordLogin, setConfirmDisablePasswordLogin] = useState(false);
  const [resumeAction, setResumeAction] = useState<OIDCSensitiveAction | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setBusyAction("load");
    try {
      const result = await oidcAdminAPI.get();
      if (signal?.aborted) return;
      const target = new URL(window.location.href);
      const shouldResume = target.searchParams.has("oidc_admin_resume");
      const callbackError = target.searchParams.get("oidc_error") || "";
      const resumed = shouldResume ? consumeOIDCResumeState(window) : null;

      setServerConfig(result);
      setForm(resumed && resumed.revision === result.revision ? resumed.form : toOIDCAdminForm(result));
      setClientSecret("");
      setClearSecret(resumed?.revision === result.revision ? resumed.clearSecret : false);
      setConfirmOIDCOnlyUsers(resumed?.revision === result.revision ? resumed.confirmOIDCOnlyUsers : false);
      setConfirmDisablePasswordLogin(resumed?.revision === result.revision ? resumed.confirmDisablePasswordLogin : false);
      if (callbackError) {
        const callbackMessages: Record<string, string> = {
          oidc_configuration_changed: text.configurationChanged,
          identity_mismatch: text.identityMismatch,
          reauth_failed: text.reauthFailed,
          session_mismatch: text.reauthFailed,
          access_denied: text.reauthFailed,
        };
        setMessage({ kind: "error", text: callbackMessages[callbackError] || text.errorFallback });
      } else if (shouldResume && (!resumed || resumed.revision !== result.revision)) {
        setMessage({ kind: "error", text: text.resumeStale });
      } else if (resumed?.requiresSecretReentry) {
        setMessage({ kind: "error", text: text.secretReentry });
      } else if (resumed) {
        setMessage({ kind: "success", text: text.formRestored });
        setResumeAction(resumed.action);
      } else {
        setMessage(null);
      }
      if (shouldResume || callbackError) {
        target.searchParams.delete("oidc_admin_resume");
        target.searchParams.delete("oidc_error");
        window.history.replaceState({}, "", target.pathname + target.search + target.hash);
      }
    } catch (error) {
      if (!signal?.aborted) setMessage({ kind: "error", text: errorMessage(error, text.loadFailed) });
    } finally {
      if (!signal?.aborted) setBusyAction(null);
    }
  }, [text]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const requestReauthentication = useCallback((action: OIDCSensitiveAction) => {
    if (user?.hasPassword) {
      setPendingAction(action);
      return;
    }
    if (!serverConfig || !form) return;
    const resume: OIDCResumeState = {
      version: 1,
      action,
      revision: serverConfig.revision,
      form,
      clearSecret,
      confirmOIDCOnlyUsers,
      confirmDisablePasswordLogin,
      requiresSecretReentry: clientSecret.length > 0,
    };
    const target = new URL(window.location.href);
    if (storeOIDCResumeState(window, resume)) {
      target.searchParams.set("oidc_admin_resume", "1");
    }
    const returnTo = oidcReauthenticationReturnTo(target);
    window.location.assign(`${apiPath("/api/auth/oidc/reauth")}?returnTo=${encodeURIComponent(returnTo)}`);
  }, [clearSecret, clientSecret.length, confirmDisablePasswordLogin, confirmOIDCOnlyUsers, form, serverConfig, user?.hasPassword]);

  const localizedError = useCallback((error: unknown) => {
    const code = typeof error === "object" && error !== null && "code" in error && typeof error.code === "string" ? error.code : "";
    const known: Record<string, string> = {
      config_revision_conflict: text.conflict,
      test_login_required: text.testRequired,
      admin_identity_required: text.adminIdentityRequired,
      recovery_password_required: text.recoveryPasswordRequired,
      configuration_invalid: text.configurationInvalid,
      probe_failed: text.probeFailed,
      oidc_only_users_confirmation_required: text.disableConfirmationRequired,
      disable_password_confirmation_required: text.disablePasswordConfirmationRequired,
    };
    return known[code] || errorMessage(error, text.errorFallback);
  }, [text]);

  const runAction = useCallback(async (action: OIDCSensitiveAction) => {
    if (!serverConfig || !form) return;
    setBusyAction(action);
    setMessage(null);
    try {
      if (action === "test") {
        const result = await oidcAdminAPI.beginTestLogin();
        window.location.assign(result.authorizationURL);
        return;
      }
      const fields = toOIDCAdminFields(form, text.ttlInvalid);
      const secret = clientSecret.length > 0 ? clientSecret : undefined;
      if (action === "save") {
        const result = await oidcAdminAPI.update({
          expectedRevision: serverConfig.revision,
          config: fields,
          clientSecret: secret,
          clearClientSecret: clearSecret || undefined,
          confirmOIDCOnlyUsers: confirmOIDCOnlyUsers || undefined,
          confirmDisablePasswordLogin: confirmDisablePasswordLogin || undefined,
        });
        setServerConfig(result);
        setForm(toOIDCAdminForm(result));
        setClientSecret("");
        setClearSecret(false);
        setConfirmOIDCOnlyUsers(false);
        setConfirmDisablePasswordLogin(false);
        setMessage({ kind: "success", text: text.saved });
      } else if (action === "probe") {
        const result = await oidcAdminAPI.probe({ config: fields, clientSecret: secret });
        setServerConfig((current) => current ? { ...current, callbackURL: result.callbackURL } : current);
        setMessage({ kind: "success", text: `${text.probeOK} (${result.discoveryLatencyMs} ms)` });
      }
    } catch (error) {
      if (isReauthenticationRequired(error)) {
        requestReauthentication(action);
      } else {
        setMessage({ kind: "error", text: localizedError(error) });
      }
    } finally {
      setBusyAction(null);
    }
  }, [clearSecret, clientSecret, confirmDisablePasswordLogin, confirmOIDCOnlyUsers, form, localizedError, requestReauthentication, serverConfig, text.probeOK, text.saved, text.ttlInvalid]);

  const confirmPassword = useCallback(async () => {
    if (!pendingAction || !password) return;
    const action = pendingAction;
    setBusyAction(action);
    try {
      await apiClient.post("/api/auth/reauth/password", { password });
      setPassword("");
      setPendingAction(null);
      await runAction(action);
    } catch (error) {
      setMessage({ kind: "error", text: localizedError(error) });
      setBusyAction(null);
    }
  }, [localizedError, password, pendingAction, runAction]);

  useEffect(() => {
    if (!resumeAction || !serverConfig || !form || busyAction !== null) return;
    const action = resumeAction;
    setResumeAction(null);
    void runAction(action);
  }, [busyAction, form, resumeAction, runAction, serverConfig]);

  if (!serverConfig || !form) {
    return (
      <div className="flex min-h-48 items-center justify-center rounded-2xl border border-border/40 bg-card text-sm text-muted">
        {busyAction === "load" ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <AlertTriangle className="mr-2 h-4 w-4 text-red-400" />}
        {busyAction === "load" ? text.loading : message?.text || text.loadFailed}
      </div>
    );
  }

  const editable = serverConfig.editable;
  const verifiedAt = serverConfig.lastVerifiedAt ? new Date(serverConfig.lastVerifiedAt).toLocaleString(locale) : text.never;
  const protectionText = serverConfig.secretProtection
    ? (serverConfig.secretProtection === "external-key-file" ? text.protectionExternal : text.protectionLocal)
    : "";
  const set = <K extends keyof OIDCAdminForm>(key: K, value: OIDCAdminForm[K]) => setForm((current) => current ? { ...current, [key]: value } : current);
  const disablingActiveOIDC = serverConfig.config.enabled && !form.enabled;

  return (
    <div className="space-y-5">
      <section className="rounded-2xl border border-border/40 bg-gradient-to-br from-accent/5 via-card to-card p-5 sm:p-6">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex gap-3">
            <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-accent/15 text-accent">
              <KeyRound className="h-5 w-5" />
            </div>
            <div>
              <h2 className="text-lg font-bold text-foreground">{text.title}</h2>
              <p className="mt-1 max-w-2xl text-sm leading-6 text-muted">{text.description}</p>
            </div>
          </div>
          <span className={`rounded-full border px-3 py-1 text-xs font-medium ${serverConfig.status === "active" || serverConfig.status === "ready" ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400" : serverConfig.status === "invalid" ? "border-red-500/30 bg-red-500/10 text-red-400" : "border-border bg-background text-muted"}`}>
            {text.status}: {statusLabels[locale][serverConfig.status]}
          </span>
        </div>
      </section>

      {!editable ? (
        <Notice icon={<LockKeyhole className="h-5 w-5" />} title={text.environmentTitle} description={text.environmentDescription} tone="amber" />
      ) : (
        <Notice icon={<ShieldCheck className="h-5 w-5" />} title={text.recoveryTitle} description={text.recoveryDescription} tone="accent" />
      )}
      {editable ? (
        <div className="space-y-1 rounded-xl border border-border/60 bg-background p-3 text-xs text-muted">
          <p>{text.recoveryCommand}</p>
          <p>{text.keyAndLogs}</p>
        </div>
      ) : null}
      {serverConfig.forcePasswordLogin ? (
        <div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-300">{text.forcePassword}</div>
      ) : null}

      {message ? (
        <div className={`rounded-xl border p-3 text-sm ${message.kind === "success" ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400" : "border-red-500/30 bg-red-500/10 text-red-400"}`}>
          {message.text}
        </div>
      ) : null}

      <section className="space-y-5 rounded-2xl border border-border/40 bg-card p-5 sm:p-6">
        <div className="grid gap-4 md:grid-cols-2">
          <Field label={text.issuerURL} value={form.issuerURL} onChange={(value) => set("issuerURL", value)} disabled={!editable} placeholder="https://id.example.com" />
          <Field label={text.clientID} value={form.clientID} onChange={(value) => set("clientID", value)} disabled={!editable} placeholder="nowen-reader" />
          <Field label={text.providerName} value={form.providerName} onChange={(value) => set("providerName", value)} disabled={!editable} placeholder="Company Login" />
          <Field label={text.publicURL} value={form.publicURL} onChange={(value) => set("publicURL", value)} disabled={!editable} placeholder="https://reader.example.com" />
          <Field label={text.scopes} value={form.scopes} onChange={(value) => set("scopes", value)} disabled={!editable} placeholder="openid profile email" />
          <label className="space-y-2 text-sm text-foreground">
            <span className="font-medium">{text.sessionTTL}</span>
            <input type="number" min="0.08333333333333333" max="720" step="0.016666666666666666" required value={form.sessionTTLHours} onChange={(event) => set("sessionTTLHours", Number(event.target.value))} disabled={!editable} className="h-10 w-full rounded-lg border border-border bg-background px-3 outline-none focus:border-accent disabled:cursor-not-allowed disabled:opacity-60" />
          </label>
        </div>

        <div className="space-y-2">
          <label className="text-sm font-medium text-foreground">{text.clientSecret}</label>
          <div className="flex gap-2">
            <div className="relative flex-1">
              <input type={showSecret ? "text" : "password"} autoComplete="new-password" value={clientSecret} onChange={(event) => { setClientSecret(event.target.value); if (event.target.value) setClearSecret(false); }} disabled={!editable} placeholder={text.secretPlaceholder} className="h-10 w-full rounded-lg border border-border bg-background px-3 pr-10 text-sm text-foreground outline-none focus:border-accent disabled:cursor-not-allowed disabled:opacity-60" />
              <button type="button" onClick={() => setShowSecret((value) => !value)} disabled={!editable} aria-label={showSecret ? text.hideSecret : text.showSecret} className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-muted hover:text-foreground disabled:opacity-40">
                {showSecret ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </div>
          <p className="text-xs text-muted">{serverConfig.clientSecretConfigured ? text.secretConfigured : text.secretMissing}{protectionText ? ` · ${protectionText}` : ""}</p>
          {editable && serverConfig.clientSecretConfigured ? (
            <label className="flex items-center gap-2 text-xs text-muted">
              <input type="checkbox" checked={clearSecret} onChange={(event) => { setClearSecret(event.target.checked); if (event.target.checked) setClientSecret(""); }} />
              {text.clearSecret}
            </label>
          ) : null}
        </div>

        <div className="space-y-2">
          <label className="text-sm font-medium text-foreground">{text.basePath}</label>
          <input readOnly value={serverConfig.basePath || "/"} className="h-10 w-full rounded-lg border border-border bg-background px-3 text-sm text-muted" />
        </div>

        <div className="space-y-2">
          <label className="text-sm font-medium text-foreground">{text.callbackURL}</label>
          <div className="flex gap-2">
            <input readOnly value={serverConfig.callbackURL} className="h-10 min-w-0 flex-1 rounded-lg border border-border bg-background px-3 text-sm text-muted" />
            <button type="button" aria-label={text.copyCallback} onClick={() => { void navigator.clipboard.writeText(serverConfig.callbackURL); setCopied(true); window.setTimeout(() => setCopied(false), 1500); }} className="flex h-10 items-center gap-2 rounded-lg border border-border px-3 text-sm text-muted hover:text-foreground">
              {copied ? <Check className="h-4 w-4 text-emerald-400" /> : <Clipboard className="h-4 w-4" />}
              <span className="hidden sm:inline">{copied ? text.copied : text.copyCallback}</span>
            </button>
          </div>
        </div>

        <div className="grid gap-3 border-t border-border/50 pt-5 sm:grid-cols-3">
          <Toggle label={text.enabled} checked={form.enabled} onChange={(value) => { setConfirmOIDCOnlyUsers(false); setForm((current) => current ? { ...current, enabled: value, disablePasswordLogin: value ? current.disablePasswordLogin : false } : current); }} disabled={!editable} />
          <Toggle label={text.autoProvision} checked={form.autoProvision} onChange={(value) => set("autoProvision", value)} disabled={!editable} />
          <Toggle label={text.disablePassword} checked={form.disablePasswordLogin} onChange={(value) => { setConfirmDisablePasswordLogin(false); set("disablePasswordLogin", value); }} disabled={!editable || !form.enabled} danger />
        </div>

        {disablingActiveOIDC && serverConfig.oidcOnlyUserCount > 0 ? (
          <div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-300">
            <p>{text.disableImpact(serverConfig.oidcOnlyUserCount)}</p>
            <label className="mt-3 flex items-start gap-2 text-xs">
              <input type="checkbox" checked={confirmOIDCOnlyUsers} onChange={(event) => setConfirmOIDCOnlyUsers(event.target.checked)} className="mt-0.5" />
              <span>{text.confirmDisableImpact}</span>
            </label>
          </div>
        ) : null}

        {!serverConfig.config.disablePasswordLogin && form.disablePasswordLogin ? (
          <div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-300">
            <p>{text.disablePasswordWarning}</p>
            <p className="mt-2 text-xs">{text.recoveryCommand}</p>
            <label className="mt-3 flex items-start gap-2 text-xs">
              <input type="checkbox" checked={confirmDisablePasswordLogin} onChange={(event) => setConfirmDisablePasswordLogin(event.target.checked)} className="mt-0.5" />
              <span>{text.confirmDisablePassword}</span>
            </label>
          </div>
        ) : null}

        <div className="flex flex-wrap items-center gap-2 border-t border-border/50 pt-5">
          <ActionButton icon={<Save className="h-4 w-4" />} label={busyAction === "save" ? text.saving : text.save} onClick={() => void runAction("save")} disabled={!editable || busyAction !== null} primary />
          <ActionButton icon={busyAction === "probe" ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />} label={busyAction === "probe" ? text.probing : text.probe} onClick={() => void runAction("probe")} disabled={!editable || busyAction !== null} />
          <ActionButton icon={busyAction === "test" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} label={busyAction === "test" ? text.testing : text.test} onClick={() => void runAction("test")} disabled={!editable || busyAction !== null || !serverConfig.clientSecretConfigured} />
          <button type="button" onClick={() => void load()} disabled={busyAction !== null} className="ml-auto h-9 rounded-lg px-3 text-xs text-muted hover:bg-background hover:text-foreground disabled:opacity-50">{text.reload}</button>
        </div>
        <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted">
          <span>{text.lastVerified}: {verifiedAt}</span>
          <span>{text.testHint}</span>
        </div>
      </section>

      {pendingAction ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="dialog" aria-modal="true" aria-labelledby="oidc-reauth-title">
          <div className="w-full max-w-md rounded-2xl border border-border bg-card p-5 shadow-2xl">
            <div className="flex items-start gap-3">
              <div className="rounded-xl bg-accent/15 p-2 text-accent"><LockKeyhole className="h-5 w-5" /></div>
              <div><h3 id="oidc-reauth-title" className="font-semibold text-foreground">{text.reauthTitle}</h3><p className="mt-1 text-sm leading-6 text-muted">{text.reauthDescription}</p></div>
            </div>
            <input type="password" autoComplete="current-password" autoFocus value={password} onChange={(event) => setPassword(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") void confirmPassword(); }} placeholder={text.currentPassword} className="mt-5 h-10 w-full rounded-lg border border-border bg-background px-3 text-sm text-foreground outline-none focus:border-accent" />
            <div className="mt-5 flex justify-end gap-2">
              <button type="button" onClick={() => { setPendingAction(null); setPassword(""); }} className="h-9 rounded-lg px-4 text-sm text-muted hover:bg-background">{text.cancel}</button>
              <button type="button" onClick={() => void confirmPassword()} disabled={!password || busyAction !== null} className="h-9 rounded-lg bg-accent px-4 text-sm font-medium text-white hover:bg-accent/90 disabled:opacity-50">{text.confirm}</button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function Field({ label, value, onChange, disabled, placeholder }: { label: string; value: string; onChange: (value: string) => void; disabled: boolean; placeholder?: string }) {
  return <label className="space-y-2 text-sm text-foreground"><span className="font-medium">{label}</span><input value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} placeholder={placeholder} className="h-10 w-full rounded-lg border border-border bg-background px-3 outline-none focus:border-accent disabled:cursor-not-allowed disabled:opacity-60" /></label>;
}

function Toggle({ label, checked, onChange, disabled, danger = false }: { label: string; checked: boolean; onChange: (value: boolean) => void; disabled: boolean; danger?: boolean }) {
  return <label className={`flex min-h-12 items-center gap-3 rounded-xl border p-3 text-sm ${danger && checked ? "border-amber-500/30 bg-amber-500/10 text-amber-300" : "border-border/60 bg-background text-foreground"}`}><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} disabled={disabled} className="h-4 w-4 accent-[var(--accent)]" /><span>{label}</span></label>;
}

function Notice({ icon, title, description, tone }: { icon: React.ReactNode; title: string; description: string; tone: "amber" | "accent" }) {
  return <section className={`flex gap-3 rounded-2xl border p-4 ${tone === "amber" ? "border-amber-500/30 bg-amber-500/10 text-amber-300" : "border-accent/30 bg-accent/5 text-foreground"}`}><div className="mt-0.5 shrink-0">{icon}</div><div><h3 className="text-sm font-semibold">{title}</h3><p className="mt-1 text-xs leading-5 opacity-80">{description}</p></div></section>;
}

function ActionButton({ icon, label, onClick, disabled, primary = false }: { icon: React.ReactNode; label: string; onClick: () => void; disabled: boolean; primary?: boolean }) {
  return <button type="button" onClick={onClick} disabled={disabled} className={`flex h-9 items-center gap-2 rounded-lg px-3 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${primary ? "bg-accent text-white hover:bg-accent/90" : "border border-border text-foreground hover:bg-background"}`}>{icon}{label}</button>;
}

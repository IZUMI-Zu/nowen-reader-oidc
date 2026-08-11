import type { OIDCAdminConfig, OIDCAdminFields } from "../api/oidc.ts";

export type OIDCSensitiveAction = "save" | "probe" | "test";

export interface OIDCAdminForm {
  enabled: boolean;
  issuerURL: string;
  clientID: string;
  providerName: string;
  scopes: string;
  publicURL: string;
  autoProvision: boolean;
  sessionTTLHours: number;
  disablePasswordLogin: boolean;
}

export interface OIDCResumeState {
  version: 1;
  action: OIDCSensitiveAction;
  revision: number;
  form: OIDCAdminForm;
  clearSecret: boolean;
  confirmOIDCOnlyUsers: boolean;
  confirmDisablePasswordLogin: boolean;
  requiresSecretReentry: boolean;
}

interface SessionStorageHost {
  sessionStorage: Pick<Storage, "getItem" | "setItem" | "removeItem">;
}

interface OIDCReturnLocation {
  pathname: string;
  search: string;
  hash: string;
}

const oidcResumeStorageKey = "nowen-reader:oidc-admin-resume:v1";
const sensitiveActions = new Set<OIDCSensitiveAction>(["save", "probe", "test"]);

export function oidcReauthenticationReturnTo(location: OIDCReturnLocation): string {
  return location.pathname + location.search;
}

export function toOIDCAdminForm(config: OIDCAdminConfig): OIDCAdminForm {
  return {
    enabled: config.config.enabled,
    issuerURL: config.config.issuerURL,
    clientID: config.config.clientID,
    providerName: config.config.providerName || "OpenID Connect",
    scopes: config.config.scopes.join(" ") || "openid profile email",
    publicURL: config.config.publicURL,
    autoProvision: config.config.autoProvision,
    sessionTTLHours: config.config.sessionTTLSeconds > 0 ? config.config.sessionTTLSeconds / 3600 : 12,
    disablePasswordLogin: config.config.disablePasswordLogin,
  };
}

export function toOIDCAdminFields(form: OIDCAdminForm, ttlInvalidMessage: string): OIDCAdminFields {
  const sessionTTLSeconds = Math.round(form.sessionTTLHours * 3600);
  if (!Number.isFinite(form.sessionTTLHours) || sessionTTLSeconds < 300 || sessionTTLSeconds > 2_592_000) {
    throw new Error(ttlInvalidMessage);
  }
  return {
    enabled: form.enabled,
    issuerURL: form.issuerURL.trim(),
    clientID: form.clientID,
    providerName: form.providerName.trim(),
    scopes: form.scopes.trim().split(/\s+/).filter(Boolean),
    publicURL: form.publicURL.trim(),
    autoProvision: form.autoProvision,
    sessionTTLSeconds,
    disablePasswordLogin: form.disablePasswordLogin,
  };
}

export function parseOIDCResumeState(raw: string | null): OIDCResumeState | null {
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as Partial<OIDCResumeState>;
    const form = value.form as Partial<OIDCAdminForm> | undefined;
    if (value.version !== 1 || !sensitiveActions.has(value.action as OIDCSensitiveAction) ||
        typeof value.revision !== "number" || !Number.isInteger(value.revision) || value.revision < 0 || !form ||
        typeof form.enabled !== "boolean" || typeof form.issuerURL !== "string" ||
        typeof form.clientID !== "string" || typeof form.providerName !== "string" ||
        typeof form.scopes !== "string" || typeof form.publicURL !== "string" ||
        typeof form.autoProvision !== "boolean" || typeof form.sessionTTLHours !== "number" ||
        !Number.isFinite(form.sessionTTLHours) || typeof form.disablePasswordLogin !== "boolean" ||
        typeof value.clearSecret !== "boolean" || typeof value.confirmOIDCOnlyUsers !== "boolean" ||
        typeof value.confirmDisablePasswordLogin !== "boolean" || typeof value.requiresSecretReentry !== "boolean") {
      return null;
    }
    return value as OIDCResumeState;
  } catch {
    return null;
  }
}

export function consumeOIDCResumeState(host: SessionStorageHost): OIDCResumeState | null {
  try {
    const state = parseOIDCResumeState(host.sessionStorage.getItem(oidcResumeStorageKey));
    host.sessionStorage.removeItem(oidcResumeStorageKey);
    return state;
  } catch {
    return null;
  }
}

export function storeOIDCResumeState(host: SessionStorageHost, state: OIDCResumeState): boolean {
  try {
    const safeState: OIDCResumeState = {
      version: 1,
      action: state.action,
      revision: state.revision,
      form: {
        enabled: state.form.enabled,
        issuerURL: state.form.issuerURL,
        clientID: state.form.clientID,
        providerName: state.form.providerName,
        scopes: state.form.scopes,
        publicURL: state.form.publicURL,
        autoProvision: state.form.autoProvision,
        sessionTTLHours: state.form.sessionTTLHours,
        disablePasswordLogin: state.form.disablePasswordLogin,
      },
      clearSecret: state.clearSecret,
      confirmOIDCOnlyUsers: state.confirmOIDCOnlyUsers,
      confirmDisablePasswordLogin: state.confirmDisablePasswordLogin,
      requiresSecretReentry: state.requiresSecretReentry,
    };
    host.sessionStorage.setItem(oidcResumeStorageKey, JSON.stringify(safeState));
    return true;
  } catch {
    return false;
  }
}

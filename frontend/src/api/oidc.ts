import { apiClient } from "@/lib/apiClient";

export type OIDCManagedBy = "environment" | "database";
export type OIDCSecretProtection = "external-key-file" | "local-key-file";

export interface OIDCAdminFields {
  enabled: boolean;
  issuerURL: string;
  clientID: string;
  providerName: string;
  scopes: string[];
  publicURL: string;
  autoProvision: boolean;
  sessionTTLSeconds: number;
  disablePasswordLogin: boolean;
}

export interface OIDCAdminConfig {
  managedBy: OIDCManagedBy;
  editable: boolean;
  status: "disabled" | "draft" | "ready" | "active" | "invalid" | "environment-managed";
  revision: number;
  clientSecretConfigured: boolean;
  secretProtection?: OIDCSecretProtection;
  forcePasswordLogin: boolean;
  callbackURL: string;
  basePath: string;
  lastVerifiedAt: string | null;
  errorCode?: string;
  oidcOnlyUserCount: number;
  config: OIDCAdminFields;
}

export interface OIDCUpdateRequest {
  expectedRevision: number;
  config: OIDCAdminFields;
  clientSecret?: string;
  clearClientSecret?: boolean;
  confirmOIDCOnlyUsers?: boolean;
  confirmDisablePasswordLogin?: boolean;
}

export interface OIDCProbeRequest {
  config: OIDCAdminFields;
  clientSecret?: string;
}

export interface OIDCProbeResult {
  issuerURL: string;
  callbackURL: string;
  discoveryLatencyMs: number;
  message: string;
}

export const oidcAdminAPI = {
  get: () => apiClient.get<OIDCAdminConfig>("/api/admin/oidc"),
  update: (request: OIDCUpdateRequest) => apiClient.put<OIDCAdminConfig>("/api/admin/oidc", request),
  probe: (request: OIDCProbeRequest) => apiClient.post<OIDCProbeResult>("/api/admin/oidc/probe", request),
  beginTestLogin: () => apiClient.post<{ authorizationURL: string }>("/api/admin/oidc/test-login"),
};

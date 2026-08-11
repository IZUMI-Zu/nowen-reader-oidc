// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { OIDCAdminConfig } from "@/api/oidc";
import { storeOIDCResumeState, type OIDCResumeState } from "@/lib/oidc-admin-state";
import { OIDCSettingsPanel } from "./OIDCSettingsPanel";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  update: vi.fn(),
  probe: vi.fn(),
  beginTestLogin: vi.fn(),
  passwordReauth: vi.fn(),
  authUser: {
    id: "admin-1",
    username: "admin",
    nickname: "Admin",
    role: "admin",
    aiEnabled: true,
    hasPassword: true,
  },
}));

vi.mock("@/api/oidc", () => ({
  oidcAdminAPI: {
    get: mocks.get,
    update: mocks.update,
    probe: mocks.probe,
    beginTestLogin: mocks.beginTestLogin,
  },
}));

vi.mock("@/lib/apiClient", () => ({
  apiClient: { post: mocks.passwordReauth },
}));

vi.mock("@/lib/auth-context", () => ({
  useAuth: () => ({ user: mocks.authUser }),
}));

vi.mock("@/lib/i18n", () => ({
  useLocale: () => ({ locale: "en" }),
}));

function managedConfig(overrides: Partial<OIDCAdminConfig> = {}): OIDCAdminConfig {
  return {
    managedBy: "database",
    editable: true,
    status: "active",
    revision: 7,
    clientSecretConfigured: true,
    secretProtection: "local-key-file",
    forcePasswordLogin: false,
    callbackURL: "https://reader.example.com/api/auth/oidc/callback",
    basePath: "/",
    lastVerifiedAt: "2026-08-11T00:00:00Z",
    oidcOnlyUserCount: 2,
    config: {
      enabled: true,
      issuerURL: "https://identity.example.com",
      clientID: "reader-client",
      providerName: "Company Login",
      scopes: ["openid", "profile", "email"],
      publicURL: "https://reader.example.com",
      autoProvision: true,
      sessionTTLSeconds: 43_200,
      disablePasswordLogin: false,
    },
    ...overrides,
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.authUser.hasPassword = true;
  window.sessionStorage.clear();
  window.history.replaceState({}, "", "/settings?tab=authentication");
});

afterEach(() => {
  cleanup();
});

describe("OIDCSettingsPanel", () => {
  test("renders environment-managed configuration read-only without refilling the secret", async () => {
    const secretSentinel = "stored-secret-must-not-enter-the-dom";
    mocks.get.mockResolvedValue({
      ...managedConfig({ managedBy: "environment", editable: false, status: "environment-managed" }),
      clientSecret: secretSentinel,
    });

    render(<OIDCSettingsPanel />);

    await screen.findByText("Managed by environment variables");
    expect((screen.getByLabelText("Issuer URL") as HTMLInputElement).disabled).toBe(true);
    const secretInput = screen.getByPlaceholderText("Enter a new Client Secret") as HTMLInputElement;
    expect(secretInput.disabled).toBe(true);
    expect(secretInput.value).toBe("");
    expect(document.body.textContent).not.toContain(secretSentinel);
  });

  test("requires and forwards the explicit OIDC-only-user confirmation", async () => {
    const current = managedConfig();
    mocks.get.mockResolvedValue(current);
    mocks.update
      .mockRejectedValueOnce({
        code: "oidc_only_users_confirmation_required",
        message: "confirmation required",
      })
      .mockResolvedValueOnce(managedConfig({
        status: "ready",
        revision: 8,
        config: { ...current.config, enabled: false },
      }));

    render(<OIDCSettingsPanel />);
    const enabled = await screen.findByRole("checkbox", { name: "Enable OIDC login" });
    fireEvent.click(enabled);
    await screen.findByText("Disabling OIDC will prevent 2 user(s) without a local password from signing in again.");

    fireEvent.click(screen.getByRole("button", { name: "Save configuration" }));
    await screen.findByText("Confirm the impact on users without local passwords before disabling OIDC.");
    expect(mocks.update).toHaveBeenNthCalledWith(1, expect.objectContaining({
      confirmOIDCOnlyUsers: undefined,
    }));

    fireEvent.click(screen.getByRole("checkbox", {
      name: "I understand the impact and still want to disable OIDC",
    }));
    fireEvent.click(screen.getByRole("button", { name: "Save configuration" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(2));
    expect(mocks.update).toHaveBeenNthCalledWith(2, expect.objectContaining({
      confirmOIDCOnlyUsers: true,
      config: expect.objectContaining({ enabled: false }),
    }));
    await screen.findByText("Configuration saved. Protocol changes require another successful test login.");
  });

  test("consumes and resumes a passwordless administrator action after OIDC reauthentication", async () => {
    mocks.authUser.hasPassword = false;
    const current = managedConfig();
    const resume: OIDCResumeState = {
      version: 1,
      action: "save",
      revision: current.revision,
      form: {
        enabled: true,
        issuerURL: "https://new-identity.example.com",
        clientID: "new-client",
        providerName: "New Login",
        scopes: "openid email",
        publicURL: "https://reader.example.com",
        autoProvision: false,
        sessionTTLHours: 8,
        disablePasswordLogin: false,
      },
      clearSecret: false,
      confirmOIDCOnlyUsers: false,
      confirmDisablePasswordLogin: false,
      requiresSecretReentry: false,
    };
    expect(storeOIDCResumeState(window, resume)).toBe(true);
    window.history.replaceState({}, "", "/settings?tab=authentication&oidc_admin_resume=1#security");
    mocks.get.mockResolvedValue(current);
    mocks.update.mockResolvedValue(managedConfig({ revision: 8 }));

    render(<OIDCSettingsPanel />);

    await waitFor(() => expect(mocks.update).toHaveBeenCalledOnce());
    expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({
      expectedRevision: 7,
      clientSecret: undefined,
      config: expect.objectContaining({
        issuerURL: "https://new-identity.example.com",
        clientID: "new-client",
        sessionTTLSeconds: 28_800,
      }),
    }));
    expect(window.sessionStorage.length).toBe(0);
    expect(window.location.search).toBe("?tab=authentication");
    expect(window.location.hash).toBe("#security");
    await screen.findByText("Configuration saved. Protocol changes require another successful test login.");
  });

  test("does not resume an action whose unsaved secret must be re-entered", async () => {
    mocks.authUser.hasPassword = false;
    const current = managedConfig();
    const resume: OIDCResumeState = {
      version: 1,
      action: "probe",
      revision: current.revision,
      form: {
        enabled: current.config.enabled,
        issuerURL: current.config.issuerURL,
        clientID: current.config.clientID,
        providerName: current.config.providerName,
        scopes: current.config.scopes.join(" "),
        publicURL: current.config.publicURL,
        autoProvision: current.config.autoProvision,
        sessionTTLHours: 12,
        disablePasswordLogin: current.config.disablePasswordLogin,
      },
      clearSecret: false,
      confirmOIDCOnlyUsers: false,
      confirmDisablePasswordLogin: false,
      requiresSecretReentry: true,
    };
    expect(storeOIDCResumeState(window, resume)).toBe(true);
    window.history.replaceState({}, "", "/settings?tab=authentication&oidc_admin_resume=1");
    mocks.get.mockResolvedValue(current);

    render(<OIDCSettingsPanel />);

    await screen.findByText(/Re-enter the Client Secret before retrying/);
    expect(mocks.update).not.toHaveBeenCalled();
    expect(mocks.probe).not.toHaveBeenCalled();
    expect(mocks.beginTestLogin).not.toHaveBeenCalled();
  });

  test("tests the saved configuration even when the unsaved form is invalid", async () => {
    mocks.get.mockResolvedValue(managedConfig());
    mocks.beginTestLogin.mockRejectedValue({ message: "provider unavailable sentinel" });

    render(<OIDCSettingsPanel />);

    const ttl = await screen.findByLabelText("Maximum OIDC session (hours)");
    fireEvent.change(ttl, { target: { value: "0.01" } });
    fireEvent.click(screen.getByRole("button", { name: "Run real test login" }));

    await waitFor(() => expect(mocks.beginTestLogin).toHaveBeenCalledOnce());
    await screen.findByText("provider unavailable sentinel");
  });
});

import { expect, test } from "vitest";

import {
  consumeOIDCResumeState,
  oidcReauthenticationReturnTo,
  parseOIDCResumeState,
  storeOIDCResumeState,
  toOIDCAdminFields,
} from "./oidc-admin-state";

const validResumeState = {
  version: 1,
  action: "save",
  revision: 7,
  form: {
    enabled: false,
    issuerURL: "https://identity.example.com",
    clientID: " opaque-client ",
    providerName: "Company Login",
    scopes: "openid profile email",
    publicURL: "https://reader.example.com",
    autoProvision: false,
    sessionTTLHours: 12,
    disablePasswordLogin: false,
  },
  clearSecret: false,
  confirmOIDCOnlyUsers: false,
  confirmDisablePasswordLogin: false,
  requiresSecretReentry: true,
} as const;

test("resume parser accepts the complete versioned state", () => {
  expect(parseOIDCResumeState(JSON.stringify(validResumeState))).toEqual(validResumeState);
});

test("resume parser rejects every missing boolean instead of creating uncontrolled inputs", () => {
  for (const field of [
    "clearSecret",
    "confirmOIDCOnlyUsers",
    "confirmDisablePasswordLogin",
    "requiresSecretReentry",
  ]) {
    const corrupted = structuredClone(validResumeState) as Record<string, unknown>;
    delete corrupted[field];
    expect(parseOIDCResumeState(JSON.stringify(corrupted)), field).toBe(null);
  }
  for (const field of ["enabled", "autoProvision", "disablePasswordLogin"]) {
    const corrupted = structuredClone(validResumeState) as { form: Record<string, unknown> };
    delete corrupted.form[field];
    expect(parseOIDCResumeState(JSON.stringify(corrupted)), `form.${field}`).toBe(null);
  }
});

test("resume parser rejects corrupt versions, actions, revisions, and numeric fields", () => {
  for (const patch of [
    { version: 2 },
    { action: "delete" },
    { revision: -1 },
    { revision: 1.5 },
    { form: { ...validResumeState.form, sessionTTLHours: null } },
  ]) {
    expect(parseOIDCResumeState(JSON.stringify({ ...validResumeState, ...patch }))).toBe(null);
  }
  expect(parseOIDCResumeState("not-json")).toBe(null);
});

test("storage helpers consume state, omit secrets, and tolerate disabled storage", () => {
  const values = new Map<string, string>();
  const host = {
    sessionStorage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => { values.delete(key); },
    },
  };
  const pollutedState = {
    ...validResumeState,
    clientSecret: "client-secret-sentinel",
    form: { ...validResumeState.form, clientSecret: "client-secret-sentinel" },
  };
  expect(storeOIDCResumeState(host, pollutedState)).toBe(true);
  const serialized = [...values.values()][0];
  expect(serialized).toBeTruthy();
  expect(serialized.includes("client-secret-sentinel")).toBe(false);
  expect(consumeOIDCResumeState(host)).toEqual(validResumeState);
  expect(values.size).toBe(0);

  const disabled = Object.create(null);
  Object.defineProperty(disabled, "sessionStorage", {
    get() { throw new Error("storage disabled"); },
  });
  expect(storeOIDCResumeState(disabled, validResumeState)).toBe(false);
  expect(consumeOIDCResumeState(disabled)).toBe(null);
});

test("form conversion validates TTL while preserving opaque client identifiers", () => {
  const fields = toOIDCAdminFields(validResumeState.form, "invalid ttl");
  expect(fields.clientID).toBe(" opaque-client ");
  expect(fields.issuerURL).toBe("https://identity.example.com");
  expect(fields.sessionTTLSeconds).toBe(43_200);
  expect(fields.scopes).toEqual(["openid", "profile", "email"]);
  expect(
    () => toOIDCAdminFields({ ...validResumeState.form, sessionTTLHours: 0.01 }, "invalid ttl"),
  ).toThrow(/invalid ttl/);
});

test("reauthentication return targets exclude browser-only fragments", () => {
  expect(oidcReauthenticationReturnTo({
    pathname: "/reader/settings",
    search: "?tab=authentication&oidc_admin_resume=1",
    hash: "#danger-zone",
  })).toBe("/reader/settings?tab=authentication&oidc_admin_resume=1");
});

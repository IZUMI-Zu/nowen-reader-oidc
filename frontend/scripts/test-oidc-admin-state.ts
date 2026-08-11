import assert from "node:assert/strict";
import test from "node:test";

import {
  consumeOIDCResumeState,
  oidcReauthenticationReturnTo,
  parseOIDCResumeState,
  storeOIDCResumeState,
  toOIDCAdminFields,
} from "../src/lib/oidc-admin-state.ts";

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
  assert.deepEqual(parseOIDCResumeState(JSON.stringify(validResumeState)), validResumeState);
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
    assert.equal(parseOIDCResumeState(JSON.stringify(corrupted)), null, field);
  }
  for (const field of ["enabled", "autoProvision", "disablePasswordLogin"]) {
    const corrupted = structuredClone(validResumeState) as { form: Record<string, unknown> };
    delete corrupted.form[field];
    assert.equal(parseOIDCResumeState(JSON.stringify(corrupted)), null, `form.${field}`);
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
    assert.equal(parseOIDCResumeState(JSON.stringify({ ...validResumeState, ...patch })), null);
  }
  assert.equal(parseOIDCResumeState("not-json"), null);
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
  assert.equal(storeOIDCResumeState(host, pollutedState), true);
  const serialized = [...values.values()][0];
  assert.ok(serialized);
  assert.equal(serialized.includes("client-secret-sentinel"), false);
  assert.deepEqual(consumeOIDCResumeState(host), validResumeState);
  assert.equal(values.size, 0);

  const disabled = Object.create(null);
  Object.defineProperty(disabled, "sessionStorage", {
    get() { throw new Error("storage disabled"); },
  });
  assert.equal(storeOIDCResumeState(disabled, validResumeState), false);
  assert.equal(consumeOIDCResumeState(disabled), null);
});

test("form conversion validates TTL while preserving opaque client identifiers", () => {
  const fields = toOIDCAdminFields(validResumeState.form, "invalid ttl");
  assert.equal(fields.clientID, " opaque-client ");
  assert.equal(fields.issuerURL, "https://identity.example.com");
  assert.equal(fields.sessionTTLSeconds, 43_200);
  assert.deepEqual(fields.scopes, ["openid", "profile", "email"]);
  assert.throws(
    () => toOIDCAdminFields({ ...validResumeState.form, sessionTTLHours: 0.01 }, "invalid ttl"),
    /invalid ttl/,
  );
});

test("reauthentication return targets exclude browser-only fragments", () => {
  assert.equal(oidcReauthenticationReturnTo({
    pathname: "/reader/settings",
    search: "?tab=authentication&oidc_admin_resume=1",
    hash: "#danger-zone",
  }), "/reader/settings?tab=authentication&oidc_admin_resume=1");
});

# OpenID Connect Configuration

English · [简体中文](./OIDC.md)

NowenReader can act as an OpenID Connect 1.0 Relying Party (RP) for browser-based single sign-on. It uses a backend-managed Authorization Code Flow with PKCE S256 for a confidential Web client.

> **Important Note**
>
> This document covers the Web application. The Flutter application does not yet support OIDC. Never reuse this confidential client or embed `OIDC_CLIENT_SECRET` in a native application.

## Before You Begin

You need:

- an OpenID Provider that supports Discovery, Authorization Code Flow, PKCE S256, and confidential clients;
- a stable external HTTPS URL for NowenReader;
- permission to register an OIDC client with the Provider;
- a long random client secret;
- a local administrator account for initial setup and recovery.

Production deployments must use HTTPS. HTTP is accepted only when `PUBLIC_URL` uses `localhost`, `127.0.0.1`, or another loopback IP. The Provider issuer must always use HTTPS.

## Recommended: Configure in the Web Admin

New deployments that do not explicitly set `OIDC_ENABLED` use database-managed configuration by default. Create a local administrator first, then open **Settings → Single sign-on**:

1. Enter the issuer, Client ID, Client Secret, public site URL, and scopes. Keep OIDC disabled and save the draft.
2. Run the Discovery check. This validates only Discovery and endpoints; it does not prove that the Client Secret is correct.
3. Run the real test login. NowenReader completes Authorization Code + PKCE and explicitly links the verified identity to the current administrator.
4. Enable OIDC after the test succeeds. Keep password login available and validate both methods in a private browser window.
5. Disable password login only after confirming the recovery path.

Protocol-field changes invalidate the previous test and require another real test login. Saved configuration takes effect immediately without a restart.

Disabling active OIDC requires the acting administrator to have a local recovery password. The page recounts users for the current issuer who have no local password and shows the exact impact; a non-zero count requires explicit confirmation. For emergency recovery, set `OIDC_FORCE_PASSWORD_LOGIN=true` in the deployment environment and restart the service.

Database-managed Client Secrets are encrypted with AES-256-GCM. Prefer an independently mounted 32-byte key through `OIDC_CONFIG_KEY_FILE`. Without one, NowenReader creates `{DATA_DIR}/secrets/oidc-config.key` with mode `0600`. A local key on the same volume protects against disclosure of the SQLite file alone, not compromise of the whole volume or host. Back up the key securely with the database.

Mount an external key in Docker Compose as follows:

```bash
openssl rand -out oidc-config.key 32
chmod 600 oidc-config.key
```

```yaml
services:
  nowen-reader:
    environment:
      OIDC_CONFIG_KEY_FILE: /run/secrets/oidc_config_key
    secrets:
      - oidc_config_key
secrets:
  oidc_config_key:
    file: ./oidc-config.key
```

Do not replace or lose this key before re-entering the Client Secret. Existing ciphertext then cannot be decrypted; OIDC remains unavailable and NowenReader does not overwrite it.

Existing environment deployments remain compatible: when `OIDC_ENABLED` is explicitly set, `OIDC_CONFIG_MODE=auto` continues to use the complete environment configuration and the Web page is read-only. You may explicitly select `environment` or `database`; fields are never mixed across sources.

For recovery, `OIDC_FORCE_PASSWORD_LOGIN=true` forces the password entry point open and overrides the Web setting. Set it and restart when the database-managed configuration is damaged or the Provider is unavailable.

## Example Assumptions

| Item | Example Value |
|:---|:---|
| NowenReader URL | `https://reader.example.com/reader/` |
| `PUBLIC_URL` | `https://reader.example.com` |
| `BASE_PATH` | `/reader` |
| Provider issuer | `https://auth.example.com` |
| Client ID | `nowen-reader` |
| Redirect URI | `https://reader.example.com/reader/api/auth/oidc/callback` |

The redirect URI is always:

```text
PUBLIC_URL + BASE_PATH + /api/auth/oidc/callback
```

`PUBLIC_URL` contains only the origin. Put the deployment path in `BASE_PATH`; do not put a path, query, or fragment in `PUBLIC_URL`.

## Provider Requirements

The Provider Discovery document must expose HTTPS `authorization_endpoint`, `token_endpoint`, and `jwks_uri` values. ID Tokens must contain standard `iss`, `sub`, `aud`, and lifetime claims and must return the requested `nonce`.

NowenReader verifies the signature, issuer, audience, `azp`, nonce, and `at_hash` when supplied by the Provider. Reauthentication and Web-admin test login use `max_age=0`, so OIDC Core also requires `auth_time`; missing, stale, or unexpectedly future values are rejected. `preferred_username`, `name`, `email`, and `email_verified` are profile hints only. The verified `(issuer, subject)` pair is the account identity; email and username never cause an automatic account merge.

## Authelia

The following structure follows Authelia's official integration documentation. It only registers the NowenReader client. Configure Authelia's mandatory signing keys, HMAC secret, storage, and other Provider options separately.

### Generate the Client Secret

Use the Authelia CLI to generate a random plaintext secret and its PBKDF2 digest together:

```bash
docker run --rm authelia/authelia:latest \
  authelia crypto hash generate pbkdf2 \
  --variant sha512 \
  --random \
  --random.length 72 \
  --random.charset rfc3986
```

The command prints two different values:

- plaintext secret: configure only as NowenReader's `OIDC_CLIENT_SECRET`;
- `$pbkdf2-sha512$...` digest: configure only as Authelia's `client_secret`.

> **Security Note**
>
> Never commit the example secret, plaintext secret, or an expanded Compose configuration. Do not configure the Authelia digest as `OIDC_CLIENT_SECRET`; NowenReader requires the original plaintext secret.

### Authelia Client

```yaml
identity_providers:
  oidc:
    # The other mandatory Authelia OIDC Provider options go here.
    clients:
      - client_id: 'nowen-reader'
        client_name: 'NowenReader'
        client_secret: '$pbkdf2-sha512$...'
        public: false
        authorization_policy: 'two_factor'
        require_pkce: true
        pkce_challenge_method: 'S256'
        redirect_uris:
          - 'https://reader.example.com/reader/api/auth/oidc/callback'
        scopes:
          - 'openid'
          - 'profile'
          - 'email'
        response_types:
          - 'code'
        grant_types:
          - 'authorization_code'
        access_token_signed_response_alg: 'none'
        userinfo_signed_response_alg: 'none'
        token_endpoint_auth_method: 'client_secret_basic'
```

The registered redirect URI must exactly match the value calculated by NowenReader, including scheme, host, port, `BASE_PATH`, path, and case.

## NowenReader

### Docker Compose

Store the raw plaintext secret in a restricted `.env` file:

```dotenv
NOWEN_READER_OIDC_CLIENT_SECRET=replace-with-the-raw-random-secret
```

```bash
chmod 600 .env
```

Configure `docker-compose.yml`:

```yaml
services:
  nowen-reader:
    environment:
      OIDC_CONFIG_MODE: 'environment'
      PUBLIC_URL: 'https://reader.example.com'
      BASE_PATH: '/reader'
      OIDC_ENABLED: 'true'
      OIDC_ISSUER_URL: 'https://auth.example.com'
      OIDC_CLIENT_ID: 'nowen-reader'
      OIDC_CLIENT_SECRET: '${NOWEN_READER_OIDC_CLIENT_SECRET:?required}'
      OIDC_DISPLAY_NAME: 'Authelia'
      OIDC_SCOPES: 'openid profile email'
      OIDC_DISABLE_PASSWORD_LOGIN: 'false'
      OIDC_AUTO_PROVISION: 'false'
      OIDC_BOOTSTRAP_ADMIN_SUBJECTS: ''
      OIDC_SESSION_MAX_AGE: '12h'
```

Recreate the service after changing its environment:

```bash
docker compose up -d --force-recreate nowen-reader
docker compose logs --tail=100 nowen-reader
```

> **Important Note**
>
> Environment-managed mode can read the client secret from a Docker/Kubernetes secret file through `OIDC_CLIENT_SECRET_FILE`; do not set it together with `OIDC_CLIENT_SECRET`. Restrict access to the mounted secret, container configuration, and Docker daemon because those privileges can still expose the credential.

## Options

### `PUBLIC_URL`

`string` · required when OIDC is enabled

The external origin used by users, such as `https://reader.example.com`. It must not contain a path, query, fragment, or user information. Production URLs must use HTTPS; HTTP is allowed only for loopback development.

### `BASE_PATH`

`string` · default: `/` · not required

The deployment subpath, for example `/reader`. Empty, `/`, and trailing-slash forms are normalized. The Web UI, API, PWA, OPDS, and OIDC callback share this prefix.

### `OIDC_ENABLED`

`boolean` · default: `false` · not required

Enables OIDC. When `true`, `PUBLIC_URL`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, and `OIDC_CLIENT_SECRET` must all be valid.

### `OIDC_ISSUER_URL`

`string` · required when OIDC is enabled

The exact issuer published by the Provider. It must be an absolute HTTPS URL without user information, query, or fragment. Do not append `/authorize`, `/.well-known/openid-configuration`, or a token endpoint.

Issuer is part of the account identity key. See [Changing Provider or Issuer](#changing-provider-or-issuer) before modifying it.

### `OIDC_CLIENT_ID`

`string` · required when OIDC is enabled

The registered confidential Web client ID. It must match the ID Token audience and the `azp` claim when present.

### `OIDC_CLIENT_SECRET`

`string` · required when OIDC is enabled · sensitive

The raw plaintext secret assigned to the confidential client. Do not use Authelia's PBKDF2 digest and never commit this value.

### `OIDC_DISPLAY_NAME`

`string` · default: `OpenID Connect` · not required

The Provider label shown on the Web login button, such as `Authelia`, `Authentik`, or `Company Login`.

### `OIDC_SCOPES`

`string` · default: `openid profile email` · not required

A space-delimited scope list. It must contain `openid`; duplicates are removed. Avoid `offline_access`: NowenReader does not persist Provider access or refresh tokens.

### `OIDC_AUTO_PROVISION`

`boolean` · default: `false` · not required

Controls unknown `(issuer, subject)` identities:

- `false`: only explicitly linked identities may log in;
- `true`: an unknown identity may create a regular `user` account;
- email and username never attach the identity to an existing account;
- Provider group or role claims never grant administrator access.

### `OIDC_BOOTSTRAP_ADMIN_SUBJECTS`

`string` · default: empty · not required

A comma-delimited allowlist of exact OIDC `sub` values. It applies only while the database has no users and requires `OIDC_AUTO_PROVISION=true`. The matching first user becomes an administrator; all other subjects are denied.

Prefer creating a local break-glass administrator and leave this empty. It does not promote existing users.

### `OIDC_SESSION_MAX_AGE`

`duration` · default: `12h` · not required

The absolute lifetime of a local OIDC session. Valid values range from `5m` to `720h` using Go duration syntax, such as `30m`, `12h`, or `168h`. Sliding renewal cannot pass this boundary.

### `OIDC_DISABLE_PASSWORD_LOGIN`

`boolean` · default: `false` · not required

Disables the default account login after OIDC acceptance. When `true`:

- `POST /api/auth/login` is rejected;
- `POST /api/auth/register` and Web self-registration are rejected;
- the Web username/password form is hidden;
- existing password hashes, sessions, and API keys are retained;
- a signed-in session may still use its local password for sensitive reauthentication;
- the backend rejects OIDC unlinking to prevent account lockout.

This option requires `OIDC_ENABLED=true`. It is fail-closed: a malformed value or another OIDC configuration error keeps password login disabled instead of silently reopening it.

> **Lockout Risk**
>
> Set this option only after testing real Provider login, administrator binding, and recovery. If the Provider or configuration fails, new logins are unavailable. The universal recovery path is to set `OIDC_FORCE_PASSWORD_LOGIN=true` and restart NowenReader. Only explicitly environment-managed deployments may instead set `OIDC_DISABLE_PASSWORD_LOGIN=false`.

## Account Bootstrap and Linking

### Recommended Flow

1. Keep `OIDC_DISABLE_PASSWORD_LOGIN=false` and `OIDC_AUTO_PROVISION=false`.
2. Create a local administrator through first-time setup.
3. Sign in and explicitly link OIDC in Account Settings.
4. Use a private browser window to confirm OIDC reaches the same administrator account.
5. Retain a local password for at least one controlled administrator.
6. Test recovery before optionally disabling password login.

### OIDC-only First User

Use this only when a local administrator cannot be created first:

```yaml
OIDC_AUTO_PROVISION: 'true'
OIDC_BOOTSTRAP_ADMIN_SUBJECTS: 'exact-provider-subject'
OIDC_DISABLE_PASSWORD_LOGIN: 'false'
```

Clear `OIDC_BOOTSTRAP_ADMIN_SUBJECTS` and restart after the first user is created. This allowlist is not a persistent role mapping.

### Link and Unlink

An existing account starts linking from an authenticated browser session. The same `(issuer, subject)` cannot belong to multiple local users, and a local user cannot link two subjects from the same issuer.

Unlinking requires password login to be enabled and the user to have a local password. It remains blocked while password login is disabled, even if stale identities from an older issuer remain in the database.

## Safely Disable Password Login

In Web/database mode, the administrator must select the dedicated dangerous-action confirmation when saving; toggling the setting alone is rejected. Environment-managed deployments use the procedure below.

Before changing the switch, confirm:

- administrator OIDC login reaches the intended existing account;
- regular-user login and provisioning policy behave as intended;
- the Provider enforces the intended MFA/access policy;
- only the expected HTTPS callback is registered;
- an administrator can edit the deployment environment and restart the service;
- setting the switch back to `false` restores local login.

Then set:

```yaml
OIDC_DISABLE_PASSWORD_LOGIN: 'true'
```

After restart, `/api/auth/me` should expose:

```json
{
  "loginMethods": {
    "password": false,
    "oidc": {
      "enabled": true,
      "displayName": "Authelia"
    }
  },
  "registrationMode": "closed"
}
```

## Reverse Proxy and Subpath

The reverse proxy must preserve `BASE_PATH` and send all callback requests to the same NowenReader instance:

```nginx
location /reader/ {
    proxy_pass http://127.0.0.1:3000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

The callback is derived only from `PUBLIC_URL` and `BASE_PATH`, never from request headers. The Provider must still register the external HTTPS URI.

If `TRUST_PROXY_HEADERS=true` is enabled, constrain trusted proxy IP/CIDR values with `TRUSTED_PROXIES`. Never trust forwarded headers from arbitrary sources.

## Operations

### Validate Configuration

Check Provider Discovery:

```bash
curl --fail --show-error \
  https://auth.example.com/.well-known/openid-configuration
```

Check NowenReader login capabilities:

```bash
curl --fail --show-error \
  https://reader.example.com/reader/api/auth/me
```

Discovery is lazy. A Provider outage does not prevent NowenReader from starting, but OIDC login returns `503` until the Provider recovers.

### Rotate the Client Secret

Only one client secret can be configured at a time:

1. change the Provider secret during a maintenance window;
2. update NowenReader's `OIDC_CLIENT_SECRET` immediately;
3. restart NowenReader;
4. verify login in a new browser session.

Existing local sessions do not depend on the client secret and remain valid until expiry or revocation.

### Changing Provider or Issuer

Issuer is part of the identity key. Migrate safely:

1. keep or re-enable password login;
2. ensure migrating users have local passwords;
3. update Provider/issuer configuration and restart;
4. users sign in locally and link the new Provider;
5. verify administrator and regular-user identities;
6. disable password login again if required.

Old issuer rows are not merged automatically and are not evidence of a currently usable login method.

### Emergency Recovery

If a Provider outage or configuration error prevents login, set:

```yaml
OIDC_FORCE_PASSWORD_LOGIN: 'true'
```

Restart NowenReader and use the retained local administrator password. Remove the recovery variable or set it to `false` after repairing the Provider or configuration. An explicitly environment-managed deployment may instead set `OIDC_DISABLE_PASSWORD_LOGIN=false`; do not delete external identities or manufacture sessions directly in the database.

## Troubleshooting

| Symptom | Likely Cause | Resolution |
|:---|:---|:---|
| Login button is missing | OIDC configuration validation failed | Review startup logs and the four required variables |
| Provider reports redirect mismatch | Registered and calculated URIs differ | Check scheme, host, port, `BASE_PATH`, path, and case |
| `account_not_authorized` | Identity is unlinked with provisioning disabled, or first subject is not allowlisted | Link a local account or correct provisioning/bootstrap policy |
| `identity_validation_failed` | Issuer, audience, `azp`, nonce, signature, or `at_hash` validation failed | Review client registration and Provider token settings |
| OIDC returns `503` | Discovery, token, or JWKS endpoint timed out or returned 5xx | Check Provider health, DNS, TLS, and container networking |
| Login creates a new account | Provider `sub`/issuer changed or the account was not linked first | Re-enable local login and explicitly link the correct identity; do not merge by email |
| A malformed switch keeps passwords disabled | Expected fail-closed behavior | Correct the boolean, or explicitly set `false` and restart for recovery |
| OIDC cannot be unlinked | Password login is disabled or the user has no local password | Enable password login and set a local password first |

## Current Limitations

- one Web OIDC issuer/client;
- Authorization Code Flow only; no Implicit, Password Grant, or Device Flow;
- no Provider access/refresh token persistence and no UserInfo request;
- no group/role claim mapping;
- logout revokes only the local NowenReader session; no RP-Initiated Logout;
- no Flutter native OIDC support yet.

## See Also

- [Authelia OpenID Connect Clients](https://www.authelia.com/configuration/identity-providers/openid-connect/clients/)
- [Authelia Client ID / Client Secret Generation](https://www.authelia.com/integration/openid-connect/frequently-asked-questions/#client-id--secret)
- [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html)
- [OAuth 2.0 Authorization Server Metadata](https://www.rfc-editor.org/rfc/rfc8414.html)
- [Proof Key for Code Exchange](https://www.rfc-editor.org/rfc/rfc7636.html)
- [OAuth 2.0 Security Best Current Practice](https://www.rfc-editor.org/rfc/rfc9700.html)

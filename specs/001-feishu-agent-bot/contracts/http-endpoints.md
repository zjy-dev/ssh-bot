# HTTP Contracts

**Date**: 2026-04-30
**Scope**: The bot exposes exactly one public HTTP surface: the OAuth callback server. Everything else (Feishu event stream, card updates) is over long-connection ws or outbound HTTPS; not public-facing.

---

## Server binding

- Binds to a loopback address (e.g. `127.0.0.1:8080`).
- An external TLS-terminating reverse proxy (cloud LB, Caddy, nginx) fronts it and publishes `https://<PublicBaseURL>`.
- Health check: `GET /healthz` returns `200 OK` with body `"ok"`.

---

## `GET /oauth/start`

Initiates an OAuth authorization-code flow for the calling user.

**Query params**:

| Name | Required | Description |
|---|---|---|
| `state` | yes | Opaque, base64url-encoded. Contains the user's `open_id` plus a random nonce, HMAC-signed by the bot. See Security below. |

**Response**:

- `302 Found` with `Location:` header pointing at `https://accounts.feishu.cn/open-apis/authen/v1/authorize?app_id=<AppID>&redirect_uri=<PublicBaseURL>/oauth/callback&state=<state>&scope=<urlencoded-space-separated-scopes>&response_type=code`
- Default `scope` set (configurable): `docx:document:readonly drive:drive:readonly contact:user.base:readonly offline_access`

**Error responses**:

| Status | Body | When |
|---|---|---|
| 400 | `invalid state` | `state` missing, malformed, or HMAC invalid |

---

## `GET /oauth/callback`

Handles the Feishu authorization-code callback.

**Query params**:

| Name | Required | Description |
|---|---|---|
| `code` | yes | Authorization code. 5-minute single-use. |
| `state` | yes | Echoed from `/oauth/start`. MUST be verified. |

**Response**:

- On success: `200 OK` with HTML `"授权成功，请回到飞书继续提问。"` (localized; closeable tab).
- On failure: `400 Bad Request` with human-readable reason; **no internal error details**.

**Side effects on success**:

1. Exchange code at `POST https://open.feishu.cn/open-apis/authen/v2/oauth/token` (JSON body per Feishu spec).
2. Encrypt `access_token` + `refresh_token` with AES-GCM using `OAUTH_ENCRYPTION_KEY`.
3. `SET bot:oauth:<open_id> <ciphertext> EXAT <refresh_expires_at>`.
4. Send a Feishu message to the user (p2p chat) confirming authorization complete, so they know to return and re-ask.

**Error handling (no success side effects occur)**:

| Case | HTTP | User sees |
|---|---|---|
| Token endpoint 4xx | 400 | "授权失败，请稍后重试" |
| Token endpoint timeout (>10s) | 504 | "授权服务暂时不可用" |
| Invalid `state` | 400 | "无效授权请求" |
| Decryption-key env missing at process start | — | process refuses to start (fail-closed) |

---

## `GET /healthz`

| — | — |
|---|---|
| Auth | none |
| Success | `200 OK`, body `ok` |
| Body format | text/plain |
| Side effects | none |

Not used by Feishu; used by load balancers and orchestrators.

---

## Security

### `state` parameter construction

```
payload = open_id || "\x00" || nonce(16 bytes, crypto/rand)
state   = base64url( payload || hmac_sha256(state_key, payload)[:16] )
```

- `state_key` is a 32-byte env-var secret (`OAUTH_STATE_KEY`), independent of `OAUTH_ENCRYPTION_KEY`.
- Verify by recomputing and `hmac.Equal` comparison.
- No state is stored server-side; HMAC is sufficient.

### Transport

- All traffic to `/oauth/*` MUST traverse TLS at the reverse proxy. Bot rejects non-proxy requests via `X-Forwarded-Proto` check (configurable; `strict_tls` flag) when deployed.
- Callback URL registered in Feishu Developer Console must exactly match `<PublicBaseURL>/oauth/callback`.

### Rate limiting

- Single IP limited to 20 callback attempts per minute (in-process token bucket). Prevents code-brute-force scanning.
- Failed callback attempts are logged with `trace_id` but **never** log `code` or token values.

### Logging

- Successful callback logs: `open_id` (hash-prefix only), `trace_id`, `scopes`, `refresh_expires_at`.
- Tokens: never logged, never printed to stderr, never persisted unencrypted.

---

## Non-endpoints (explicitly not served)

- No `/events` inbound for Feishu event subscriptions — events arrive via ws long-connection.
- No management/admin endpoints in v1 (see spec "Out of Scope").
- No `/revoke` endpoint in v1 (see spec "Out of Scope"; users revoke via Feishu console).

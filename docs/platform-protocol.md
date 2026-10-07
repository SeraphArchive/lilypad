# Platform protocol

The platform shim in `internal/portal/shim.go` supplies the account/takeover and
wallet surface used by compatible Steam clients. It serves plain JSON on
`/v1.0/*`; game routes use a separate encoded body on `/api/*`.
All identifiers, credentials and tokens shown here are placeholders.

## Identity and login

Portal registration stores password hashes and a durable XUID. A takeover code
and separate password let the client resolve that XUID after installation.
The shim binds the requesting platform identity to the Portal account. Stored
requestor bindings survive server restarts; post-migration requestors can also
use the `lilypad-<xuid>` form. XUID is mapped to numeric game `userId` by
`/api/user/client/id` and `/api/user/migration/id`.

A typical takeover sequence is:

```text
GET  /v1.0/auth/now
POST /v1.0/auth/authorize
GET  /v1.0/auth/x_uid
POST /v1.0/migration/code/verify
POST /api/user/migration/prepare
POST /api/user/migration/id
GET  /v1.0/auth/x_uid
POST /api/user/client/id
POST /api/app/start
POST /api/user/confirm
POST /api/user/pull
```

Individual client versions may add or reorder calls. Current server routing and
tests define implemented behavior. The game session requires a per-process
`x-lilypad-session`; later game requests use the returned numeric account identity.

The title-screen takeover path can run before `/auth/authorize`. A fresh SDK
then sends `/migration/code/verify` without `xoauth_requestor_id`; its subsequent
`/migration` request also omits the optional `dst_uuid`. After credential and
wallet checks succeed, the shim persists a binding for the returned `src_uuid`.
The SDK adopts that UUID and saves its new key pair after `/migration` succeeds.
An existing requestor, when present, continues to be bound as before. Failure to
persist either form of binding rejects verification.

## Payload contracts

Platform success uses a root string `result: "OK"`; errors use a non-success
result. `entry` holds nested payloads where required by the route. `auth/now`
must return `t` as a JSON number, not a string:

```json
{"result":"OK","t":1700000000}
```

`auth/x_uid` returns nonempty string fields `x_uid` and `x_app_id` after identity
resolution. For a new identity, `auth/authorize` accepts the SDK's nonempty
`device_id`, `token` (generated public key), and `id_token` strings. For an
existing OAuth requestor, the SDK sends only `id_token`; the server accepts
this form and preserves the requestor. It returns a root-level `uuid`:

```json
{"result":"OK","uuid":"<device-requestor-id>"}
```

The SDK needs that UUID to persist its authentication identity and key pair.
Returning only `result: "OK"` leaves an authorized installation without a saved
requestor identity. For a fresh installation, the UUID is derived
from framed application, device and public-key fields; refreshed Steam tickets
do not rotate it. Different public keys separate isolated profiles on one device.
An existing OAuth `xoauth_requestor_id` is preserved during reauthorization,
including the identity adopted after account transfer.

The fresh device UUID cannot be interpreted as a player XUID and selects no
account. Valid takeover credentials are still required before its durable
requestor binding is established. Direct title-screen takeover does not require
this preliminary authorization step. This compatibility shim does not verify
Steam tickets or official
OAuth signatures; the UUID is an installation handle, not proof of account access.

`migration/code/verify` accepts:

```json
{"migration_code":"<code>","migration_password":"<wire-password>"}
```

The password wire transform is `ROT13(reverse(base64(UTF8(password))))`.
Decode with `base64decode(reverse(ROT13(wire)))`, then verify the stored Argon2
hash. The wire encoding is reversible; use HTTPS and keep credentials out of logs.

The successful response includes `src_uuid`, `src_x_uid`, `migration_token` and
decimal string wallet fields `balance_charge_gem`, `balance_free_gem` and
`balance_total_gem`. A transfer request on `/v1.0/migration` carries `device_id`,
`dst_uuid`, `src_uuid`, `migration_token` and the client public-key `token`.
These compatibility fields do not imply official device/payment settlement.

## Wallet and compatibility routes

`payment/balance` returns stored free/paid/total quartz as strings under `entry`.
`migration/code/verify` reports the same balance for the resolved account.
The adapter reads PostgreSQL; new accounts begin at zero. Read errors must be
checked against current handler behavior rather than treated as free currency.

`payment/productlist` exposes a compatibility price catalogue filtered using
configured master product releases. Price entries are not purchase settlement.
`payment/purchase/steam/userinfo` reports configured country/currency;
`payment/purchase/alert/setting`, `moderate/keywordlist` and linked-active routes
provide limited compatibility responses. Additional endpoints are registered in
`Shim.Routes`; registration alone does not promise complete platform behavior.

`app/start` belongs to the game API. Its asset version/hash comes from configuration
or a matching client version report. The asset CDN and game installation remain
external to this server; avoid putting build-specific hashes or install paths
in public configuration examples.

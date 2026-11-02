# Single sign-on (OIDC)

Hogwatch can sign users in through an OpenID Connect provider such as Authentik.

Add to `.env`:

```bash
HOGWATCH_BASE_URL=https://disk.example.com
HOGWATCH_AUTH_OIDC_ISSUER=https://auth.example.com/application/o/hogwatch/
HOGWATCH_AUTH_OIDC_CLIENT_ID=...
HOGWATCH_AUTH_OIDC_CLIENT_SECRET=...
HOGWATCH_AUTH_OIDC_GROUPS_CLAIM=groups
HOGWATCH_AUTH_OIDC_ADMIN_GROUP=hogwatch-admins
```

In the provider create an OAuth2/OIDC application with this redirect URI:

```
https://disk.example.com/api/v1/auth/oidc/callback
```

with the `openid`, `email` and `profile` scopes, plus a groups claim if you want group admins.

## Linking accounts

Hogwatch links an existing account to a provider login only when the provider says `email_verified: true`. If your provider sends a constant value, fix the property mapping first. After linking, accounts match on issuer and subject only.

Admins who sign in with SSO get a shorter session (12 h by default, `HOGWATCH_SESSION_OIDC_ADMIN_TIMEOUT`).

## Private certificate authority

The image trusts public certificate authorities only. If your provider uses a private CA, mount the CA bundle into `hogwatch-web` through a `compose.override.yaml` and point `SSL_CERT_FILE` at it:

```yaml
services:
  hogwatch-web:
    environment:
      SSL_CERT_FILE: /certs/ca.pem
    volumes:
      - ./ca.pem:/certs/ca.pem:ro
```

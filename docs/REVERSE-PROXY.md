# Reverse-proxy examples

Activity-Relay Directory does not require, install, enable, or reload a reverse
proxy. The examples in `contrib/nginx/`, `contrib/apache/`, and
`contrib/caddy/` are optional starting points for operators who already use one
of those servers.

## Assumptions

The examples use:

```text
Public URL:       https://directory.example.org
Directory bind:  127.0.0.1:8080
Maximum body:    1 MiB at the proxy
```

Replace the reserved example hostname and certificate paths. Configure the
service itself with:

```text
DIRECTORY_LISTEN_ADDRESS=127.0.0.1:8080
DIRECTORY_PUBLIC_BASE_URL=https://directory.example.org
DIRECTORY_REGISTRATION_ENABLED=false
```

Keep the Go listener on loopback when the proxy is the public entry point. The
proxy's 1 MiB body ceiling matches the service's absolute configurable maximum;
the service default remains the smaller 64 KiB limit.

## Signature-sensitive forwarding

Future register, heartbeat, and unregister requests use RFC 9421 signatures.
The proxy must preserve the public `Host`, path, and query exactly and must not
redirect a signed POST between different paths or authorities. TLS terminates
at the proxy; the configured public base URL supplies the external HTTPS
authority used for verification.

The examples preserve the incoming host and forward the original request path.
Do not add a path suffix to Nginx `proxy_pass`, change Apache's mapping, or
override Caddy's upstream `Host` without repeating signature compatibility
tests.

The examples also overwrite `X-Real-IP` from their connection metadata. The
dormant source resolver accepts that field only when the direct socket peer is
an explicitly trusted proxy; otherwise it ignores all forwarding fields. It
does not use appendable `Forwarded` or `X-Forwarded-For` chains as a security
identity. Prefer trusting only the exact local proxy addresses
`127.0.0.1/32` and, when used, `::1/128`. Do not trust a whole LAN merely to
permit LAN clients: private client addresses are already valid sources. Nested
proxies or CDNs require a separately reviewed trust chain. See
`docs/ADMISSION.md`.

## Nginx

Source example:

```text
contrib/nginx/activity-relay-directory.conf.example
```

On a Debian or Ubuntu host, adapt it under `sites-available`, enable it through
the normal `sites-enabled` link, and validate before reload:

```sh
sudo nginx -t
sudo systemctl reload nginx
```

The example applies bounded connections and request rate per source. Review the
limits against real traffic before public registration is enabled.

## Apache HTTP Server

Source example:

```text
contrib/apache/activity-relay-directory.conf.example
```

Enable the documented modules, adapt the virtual host, and validate before
reload:

```sh
sudo a2enmod headers http2 proxy proxy_http rewrite ssl
sudo apache2ctl configtest
sudo systemctl reload apache2
```

Apache core does not provide the example Nginx request-rate behavior. Apply an
equivalent reviewed policy through an appropriate module or network layer
before enabling registration publicly.

## Caddy

Source example:

```text
contrib/caddy/Caddyfile.example
```

Merge the site block into the system Caddyfile or import it, then validate and
reload:

```sh
sudo caddy fmt --diff /etc/caddy/Caddyfile
sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl reload caddy
```

Caddy provisions certificates automatically only when DNS and inbound ports 80
and 443 reach the server. The package and repository do not request or manage
certificates.

## Verification

The initial scaffold exposes only these read-only endpoints:

```text
/healthz
/readyz
/v1/status
```

After enabling one proxy, verify:

```sh
curl --fail --silent --show-error https://directory.example.org/healthz
curl --fail --silent --show-error https://directory.example.org/readyz
curl --fail --silent --show-error https://directory.example.org/v1/status |
  python3 -m json.tool
```

Registration paths remain unavailable until their security and storage gates
are implemented. Proxy installation alone must not enable registration.

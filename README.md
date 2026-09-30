# Uncloud storage module for Caddy

This is a [Caddy storage module](https://caddyserver.com/docs/caddyfile/options#storage) for Caddy running as a reverse
proxy in an [Uncloud](https://github.com/psviderski/uncloud) cluster. It lets Caddy instances on different machines
share TLS certificates, private keys, and ACME challenge tokens through Uncloud's cluster store. It uses distributed
locks to coordinate certificate issuance.

## Why it's needed

When you run multiple instances of Caddy behind a load balancer or multi-address DNS record, an ACME challenge may reach
any Caddy instance. With Caddy's default local storage, only the instance that starts issuing a certificate has the
token needed to answer the challenge. If it reaches another instance, validation fails and Caddy retries. This can take
a long time or fail altogether, depending on how traffic is routed.

See [issue #31](https://github.com/psviderski/uncloud/issues/31) for more context on this problem.

With this shared storage, any Caddy instance can read the challenge token from the cluster store and complete the ACME
challenge. Once one instance obtains a certificate, the others can use it too.

## Usage

Uncloud runs Caddy as a [global](https://uncloud.run/docs/guides/deployments/deploy-global-services) service on every
machine in the cluster. To use this module, deploy a Caddy image that includes it and add `storage uncloud` to Caddy's
global options. You can use the pre-built image or build your own if you need another compatible Caddy version or other
modules.

See [Managing Caddy](https://uncloud.run/docs/concepts/ingress/managing-caddy) in the Uncloud docs for more information
on Caddy deployments.

### Using the pre-built image

`ghcr.io/unlabs-dev/caddy-uncloud` image is built from the [Dockerfile](Dockerfile) in this repository and includes the
Uncloud storage module and a few other popular modules such as [caddy-l4](https://github.com/mholt/caddy-l4) and
[cloudflare](https://github.com/caddy-dns/cloudflare).

Create a `Caddyfile` with the global storage option:

```caddyfile
{
    storage uncloud
}
```

Deploy Caddy with the pre-built image and your global config.

> [!NOTE]
> You need `uc` version 0.21.0 or newer to deploy using `uc caddy deploy` command. If you have an older version (`uc
> version`), [upgrade](https://uncloud.run/docs/getting-started/install-cli/) it or use `uc deploy` with a
> [`compose.yaml`](#composeyaml) file instead.

```shell
uc caddy deploy --image ghcr.io/unlabs-dev/caddy-uncloud:0.1.0 --caddyfile Caddyfile
```

Uncloud combines your global config with the sites it generates for published services. Check the resulting Caddyfile
and service status:

```shell
uc caddy config
uc inspect caddy
```

### Building and deploying a custom image

Build your own image if you need other Caddy modules or want to choose a compatible Caddy version. Create the following
`.env`, `Dockerfile`, and `compose.yaml` in the same directory.

#### .env

Set the desired upstream Caddy version in `.env`:

```dotenv
CADDY_VERSION=2.11.4
```

#### Dockerfile

The Dockerfile builds the chosen Caddy version with the Uncloud storage module using
[xcaddy](https://github.com/caddyserver/xcaddy). You can add any other modules to the `xcaddy build` command. Pin module
versions if you want to avoid breaking changes in the future.

```dockerfile
ARG CADDY_VERSION

FROM caddy:${CADDY_VERSION}-builder AS builder
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    xcaddy build \
        --with github.com/unlabs-dev/caddy-uncloud/storage
    # Include other Caddy modules you need here:
    #   --with github.com/caddy-dns/cloudflare@v0.2.4

FROM caddy:${CADDY_VERSION}
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
```

#### compose.yaml

The Compose file defines a custom build and deploy configuration for the Caddy service:

```yaml
services:
  caddy:
    build:
      args:
        CADDY_VERSION: ${CADDY_VERSION}
      # If all your cluster machines use the same architecture as your local machine, you can omit `platforms`.
      platforms:
        - linux/amd64
        - linux/arm64
    image: local/caddy-uncloud:${CADDY_VERSION}-{{date "20060102-150405"}}
    environment:
      # unix// is not a typo. Caddy uses network/address format, not a unix:// URL.
      CADDY_ADMIN: unix//run/caddy/admin.sock
    healthcheck:
      test: curl -fsS -o /dev/null --unix-socket /run/caddy/admin.sock http://localhost/config/
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
      start_interval: 1s
    volumes:
      - /var/lib/uncloud/caddy:/etc/caddy:ro
      - /run/uncloud/api:/run/uncloud/api:ro
      - /run/uncloud/caddy:/run/caddy
    x-ports:
      - 80:80@host
      - 443:443@host
      - 443:443/udp@host
    x-caddy: |
      {
          storage uncloud
      }
    deploy:
      mode: global
```

Keep the admin address, mounts, and host ports as shown. Uncloud combines your global config `x-caddy` with the sites it
generates for published services and writes the resulting Caddyfile to the mounted `/etc/caddy`. The storage module
connects to Uncloud API through the Unix socket at `/run/uncloud/api/uncloud.sock`.

Run `uc deploy` from the directory containing these three files. Then check that `storage uncloud` appears in the
updated Caddy config:

```sh
uc deploy
uc caddy config
```

`uc deploy` builds the image with your local Docker, pushes it directly to cluster machines, and updates the Caddy
service. You do not need a registry.

## Storage options

The module has two optional settings:

| Caddyfile option      | JSON field | Default                         | Purpose                                                                            |
|-----------------------|------------|---------------------------------|------------------------------------------------------------------------------------|
| `socket <path>`       | `socket`   | `/run/uncloud/api/uncloud.sock` | Path to the local Uncloud API socket inside the Caddy container.                   |
| `lock_ttl <duration>` | `lock_ttl` | `20s`                           | Lifetime of a distributed lock lease. The module renews held leases automatically. |

For example, to set both options explicitly:

```caddyfile
{
    storage uncloud {
        socket /run/uncloud/api/uncloud.sock
        lock_ttl 20s
    }
}
```

## How storage works

The module sends Caddy's storage operations to the local Uncloud API. Uncloud stores and replicates the data across the
cluster. The replicated store is eventually consistent, so reads may return older data than the most recent write.

To ensure that any Caddy instance reads the latest data and coordinates with other instances when issuing certificates,
it uses distributed locks. After acquiring a lock, the module waits for its local store replica to catch up with
versions reported by responding machines before Caddy reads or writes under that lock. This still doesn't provide strong
guarantees but it's sufficient for Caddy's use case.

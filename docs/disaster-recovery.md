# Disaster recovery — restoring Miabi onto a fresh host

This is the runbook for the worst day: the machine running Miabi is gone, and you
have a bucket and a passphrase.

Read the first section before you need it. The rest is the procedure.

---

## Is a passphrase required?

No. Platform backups work without one — the artifacts are simply written
unencrypted, and the recovery point carries no sealed identity envelope.

The consequence is worth understanding before you choose: **without a passphrase,
a recovery point cannot rebuild the platform on a fresh host.** Nothing in it
carries `MIABI_ENCRYPTION_KEY`, so a restore elsewhere would produce a platform
that lists every workspace and cannot decrypt a single secret. Such a backup is
still useful — it restores onto a host that has the original key, which covers a
bad migration or a corrupted database — but it is not disaster recovery.

Set a passphrase and both follow automatically: artifacts are encrypted, and the
encryption key is sealed into every recovery point.

## What you must keep outside the platform

With a passphrase set, two things — and losing either makes the backups useless:

1. **The backup passphrase.** It encrypts the artifacts *and* seals the identity
   envelope. Miabi stores it encrypted in its own database — which is exactly what
   a disaster destroys — so a copy must live somewhere else: a password manager, a
   sealed envelope, your configuration management.
2. **Access to the bucket.** The S3 credentials Miabi uses are also inside the
   backup. Keep a set you can reach independently.

That is the whole custody story, because the master encryption key
(`MIABI_ENCRYPTION_KEY`) travels *inside* the identity envelope. You do not have
to keep it separately — the passphrase opens it.

> If you turn **off** "Seal the identity envelope" — or run without a passphrase —
> that stops being true: the key no longer travels, and a recovery point can then
> only be restored onto a host that still has the original
> `/etc/miabi/stack.yaml`. Back that file up if you choose this.

---

## Configuring platform backup

S3 or MinIO is the only destination. A backup written to a disk on the machine it
protects cannot be read after that machine is gone.

### From the environment (recommended)

Setting the target in the stack manifest means a rebuilt host is already pointed
at its bucket on first boot, with nothing to retype under pressure. These win over
anything set in the UI, which shows them read-only.

```bash
MIABI_PLATFORM_BACKUP_S3_ENDPOINT=https://s3.eu-central-1.amazonaws.com
MIABI_PLATFORM_BACKUP_S3_BUCKET=acme-miabi-dr
MIABI_PLATFORM_BACKUP_S3_REGION=eu-central-1
MIABI_PLATFORM_BACKUP_S3_ACCESS_KEY=...
MIABI_PLATFORM_BACKUP_S3_SECRET_KEY=...
MIABI_PLATFORM_BACKUP_S3_USE_SSL=true
MIABI_PLATFORM_BACKUP_S3_FORCE_PATH_STYLE=false   # true for MinIO

# Optional layout. Defaults: no root prefix, "databases", "volumes".
MIABI_PLATFORM_BACKUP_PATH=prod
MIABI_PLATFORM_BACKUP_DATABASE_PATH=databases
MIABI_PLATFORM_BACKUP_VOLUME_PATH=volumes

# Encryption + contents. The passphrase is OPTIONAL; setting it turns on both
# artifact encryption and the sealed identity envelope (see above).
MIABI_PLATFORM_BACKUP_PASSPHRASE=...              # optional
MIABI_PLATFORM_BACKUP_ENCRYPT=true
MIABI_PLATFORM_BACKUP_INCLUDE_IDENTITY=true
MIABI_PLATFORM_BACKUP_INCLUDE_TENANT_DATA=false   # see "Tenant data" below

# Schedule + retention (retention counts whole recovery points, not files).
MIABI_PLATFORM_BACKUP_SCHEDULE="0 3 * * *"
MIABI_PLATFORM_BACKUP_MAX=14
MIABI_PLATFORM_BACKUP_RETENTION_DAYS=30
```

### The bucket layout

```
s3://acme-miabi-dr/prod/
  recovery-mbdr_mbi_…_20260731T030000Z.xml    ← info file: what this recovery point contains
  identity-mbdr_mbi_…_20260731T030000Z.mbid   ← sealed: encryption key, JWT secret, identity
  databases/
    miabi_20260731_030102.sql.gz.gpg          ← the control plane
    tenants/<workspace>/…                     ← tenant dumps (when enabled)
  volumes/
    mb-node-gateway-providers_….tar.gz.gpg
    tenants/<workspace>/…                     ← tenant volumes (when enabled)
```

### The two control files at the root

Every recovery point writes two small files beside the artifacts, and only one of
them is encrypted. That is deliberate:

| File | Encrypted? | What it is |
|---|---|---|
| `identity-<ref>.mbid` | **Yes**, always | The sealed identity envelope: `MIABI_ENCRYPTION_KEY` and the JWT secret. Encrypted under the backup passphrase, because it *is* the platform. Without it a recovery point cannot rebuild on a fresh host. |
| `recovery-<ref>.xml` | **No**, by design | The index: which artifacts exist, the version that produced them, and the encryption-key fingerprint. |

The index stays readable because it is what makes a bucket self-describing.
`miabi restore` reads it before it has a database or anything to check a
passphrase against, and `miabi recovery-points` uses it to tell you what a bucket
holds when all you have is an S3 client.

It carries **no key, credential or data**. `kek_fingerprint` is an HMAC that
identifies which encryption key a recovery point belongs to without revealing it —
that is what lets a restore refuse a mismatched envelope *before* writing a
platform full of undecryptable secrets. The workspace and database names it lists
are already visible in the object keys next to it.

### Tenant data

"Include tenant data" adds every workspace's databases and volumes. Without it a
recovery point restores **the control plane only**: workspaces, apps, routes,
secrets and users come back, and their data does not. Decide which you are buying,
because the difference is invisible until you need it.

---

## Prove it works before you need it

Open **Admin → Platform Backup → Recovery points**, pick one, and press
**Verify**. Enter the passphrase from your password manager — not the stored one.
Verification confirms every artifact is still in the bucket and that the envelope
opens with the passphrase you actually hold.

A recovery point nobody has ever opened is a hypothesis.

---

## The recovery

On a fresh host with Docker installed and nothing else:

```bash
docker run --rm -it \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /etc/miabi:/etc/miabi \
  miabi/miabi:<version> \
  recovery-points \
    --from s3://acme-miabi-dr/prod \
    --access-key … --secret-key …
```

That lists what the bucket holds. Then, still on the fresh host:

```bash
docker run --rm -it \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /etc/miabi:/etc/miabi \
  -v /root/miabi-dr.pass:/run/pass:ro \
  miabi/miabi:<version> \
  restore \
    --from s3://acme-miabi-dr/prod \
    --ref mbdr_mbi_…_20260731T030000Z \
    --passphrase-file /run/pass \
    --access-key … --secret-key … \
    --dry-run
```

`--dry-run` validates everything and creates nothing. It refuses on a wrong
passphrase, a missing artifact, a backup newer than this binary, a host that
already has an install, or — the important one — an identity envelope whose
encryption key is not the one that recovery point was taken under.

Drop `--dry-run` to run it. In order, it:

1. writes `/etc/miabi/stack.yaml` with the **recovered** encryption key and JWT
   secret, and **fresh** database and Redis passwords;
2. starts Postgres and Redis;
3. restores the control-plane dump into the empty database;
4. restores the platform volumes;
5. marks the platform as recovering;
6. starts the control plane and gateway.

Omit `--ref` to take the newest recovery point. Use `--domain` to serve on a
different hostname.

## After the restore

The platform comes up **quiesced**: schedules do not fire and nothing redeploys.
This is deliberate — DNS still points at the machine you are recovering from, and
a platform that starts requesting certificates for addresses that resolve
elsewhere makes the outage worse.

1. Sign in. A banner on **Admin → Platform Backup** shows the recovery state.
2. Press **Reconcile now**. It resets stale node identity, recreates networks from
   the ledger, starts the database containers, restores tenant data (when the
   recovery point carried it), redeploys apps and re-renders routes — then reports
   what it could not recover.
3. **Read the report.** What it lists as unrecoverable is genuinely gone; nothing
   further in this runbook brings it back.
4. Point DNS at the new host and wait for it to resolve.
5. Press **Complete recovery**. Schedules and certificate issuance resume.

## Cloning instead of recovering

To stand up a copy while the original still runs:

```bash
miabi restore --from … --ref … --passphrase-file … --clone --domain staging.example.com
```

`--clone` mints a new install ID and requires `--domain`. Both are non-negotiable:
two live platforms sharing an install ID break licensing, and a clone that
inherited production's hostnames would race it for DNS and certificates. The clone
needs its own Enterprise license.

## What never comes back

- **Images in a filesystem-backed registry.** Their blobs lived on the machine
  that is gone. Apps that build from Git rebuild; apps whose image was pushed by
  external CI and exists nowhere else do not. Run the registry on S3 storage if
  your images matter for recovery.
- **Remote nodes and runners.** Their enrolment belonged to the old host. Re-enrol
  them; the reconcile report lists which.
- **In-flight background jobs.** Redis starts empty. Schedules re-register from
  the database.

## Recovering onto the original host

If the machine survived and only the database is damaged, restore in place from
**Admin → Platform Backup** instead. That path keeps the existing
`/etc/miabi/stack.yaml`, so the encryption key is already correct — the identity
envelope is not needed.

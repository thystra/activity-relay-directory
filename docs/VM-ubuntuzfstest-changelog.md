# `ubuntuzfstest` VM and Baseline Changelog

This file records changes to the shared `ubuntuzfstest` validation VM and to reusable recovery/clean-room baselines made from it.

The cross-project canonical copy is maintained in `wp-plugin-template`. Project repositories may mirror this file so their release and acceptance procedures retain local visibility into shared-host state. When a shared-host change affects more than one project, update the canonical template record and mirror the same factual entry into affected project documentation.

## Change-control rules

`ubuntuzfstest` is shared validation infrastructure. WordPress projects use it for Docker testing and Plugin Check work, and non-WordPress projects may use it for clean-room/package/container acceptance. A project must not silently redefine the VM's reusable baseline for every other consumer.

For every baseline or shared-capability change, record:

* the date and reason for the change;
* the exact ZFS baseline tag or tags actually created;
* root-pool and boot-pool coverage;
* EFI evidence or backup status;
* running kernel and significant host-package state when relevant;
* generic capabilities intentionally included in the baseline;
* project-specific state explicitly excluded from the baseline;
* any important state changed **after** the baseline was created;
* restoration limitations or follow-up work;
* which baseline is currently preferred for future recovery.

Never edit a historical entry so that it appears a baseline contained a change that actually occurred afterward. Add a new entry instead.

A baseline is not considered the preferred shared-host recovery point merely because its snapshots exist. It must represent the intended generic validation-host capabilities for current consumers.

## Known baseline history

### Legacy recovery points — pre-v2

Known older recovery evidence includes:

* root-side snapshots using the historical `ard-rc1-pre-acceptance` tag;
* boot-pool snapshots using the historical `baseline` tag.

These predate the paired-baseline procedure established during Activity-Relay Directory RC3 acceptance. They are retained as historical/recovery evidence and must not be treated as equivalent to a modern paired root+boot+EFI baseline without direct inspection.

### 2026-08-22 — `ard-cleanroom-baseline-v2`

**Status:** authenticated recovery/acceptance baseline; not the preferred long-term shared-host baseline.

The VM was updated and normally rebooted into kernel `7.0.0-30-generic`. A project-neutral clean-room state was proved before the new baseline was created.

Baseline coverage:

* recursive root baseline:
  `rpool/ROOT/ubuntu_gg0ebn@ard-cleanroom-baseline-v2`;
* recursive boot-pool baseline:
  `bpool@ard-cleanroom-baseline-v2`;
* 16 root-tree datasets and 3 boot-pool datasets authenticated;
* both pool snapshots recorded the same creation second;
* EFI System Partition recorded separately because it is outside ZFS;
* boot files and the kernel `7.0.0-30-generic` module tree authenticated against the snapshots;
* raw EFI image and file-level EFI checksum evidence completed during the continuation workflow;
* package authority reconstructed from the snapshot's own dpkg database after the original package-manifest capture was proven malformed.

The baseline intentionally excludes Activity-Relay Directory candidate installation/container state and other project-specific acceptance residue.

**Important limitation discovered afterward:** this v2 baseline predates adding the normal test account `alan` to the `docker` group. Restoring v2 therefore removes direct Docker-socket access for `alan`, even though the Docker daemon itself is healthy and `sudo docker` works.

Because WordPress projects use `ubuntuzfstest` for Docker testing and Plugin Check work, direct Docker access for the normal test account is considered a generic shared-host capability. For that reason, v2 remains valid historical/recovery and ARD RC3 acceptance evidence, but it should be superseded by a newer shared baseline before future routine rollback use.

### 2026-08-22 — post-v2 shared-infrastructure correction: Docker access

**Baseline membership:** NOT contained in `ard-cleanroom-baseline-v2`.

After restoring/testing against v2, the normal account state was observed as:

* `/var/run/docker.sock` owned by `root:docker`;
* `docker` group present;
* `alan` absent from the `docker` group;
* `sudo docker info` successful;
* direct `docker` access denied.

The generic shared-host capability was restored with:

```text
sudo usermod -aG docker alan
```

After a fresh login, `alan` was a member of group `docker` and:

```text
docker info
```

succeeded without `sudo`.

This change must be included in the next preferred shared baseline. It must not be retroactively attributed to v2.

## Planned next preferred baseline — v3

**Status:** planned; not yet created.

The exact ZFS snapshot tag must be recorded here at creation time rather than inferred in advance.

Before creating v3, the VM should satisfy and record at least these invariants:

* `alan` is a member of the `docker` group;
* `docker info` succeeds as `alan` without `sudo`;
* Docker daemon health is good;
* WordPress Docker/Plugin Check prerequisites are functional;
* automatic APT maintenance has been restored to its intended normal state after any quiesced RC acceptance window;
* package state is internally consistent (`dpkg --audit`, `apt-get check`);
* the preferred kernel boots normally and has matching boot files/modules;
* rpool and bpool are healthy;
* no Activity-Relay Directory candidate package, staged test configuration, persistent acceptance container/volume, or other project-specific test residue is present;
* no WordPress-project-specific test residue is preserved as generic baseline state;
* ports/resources reserved by prior tests are released;
* root and boot pools are snapshotted as one documented logical baseline;
* the EFI System Partition is separately evidenced/backed up;
* baseline manifests/checksums are generated from correctly quoted/status-aware commands;
* the baseline is tested for the generic capabilities relied upon by its current consumers.

Once v3 is authenticated, mark it as the preferred shared-host recovery baseline. Retain v2 and older snapshots as historical/recovery evidence until a separate reviewed retention decision is made.

## Future entries

Add new entries chronologically. Each entry should distinguish clearly between:

1. **baseline-contained state** — what a rollback to that baseline actually restores;
2. **post-baseline host changes** — changes made later that are not present in that snapshot;
3. **project-specific acceptance state** — temporary state that should not become part of a generic baseline.

# Decisions

| ADR | Decision | Status |
|---|---|---|
| [001](001-shrooms-backup.md) | Shrooms Backup: a daemon of its own, restic over Logos Storage, mirrors chosen per machine | accepted; live: proteus, the laptop and jimmy-crib (test data) back up; proteus, scribe and the VPS keep copies |
| [002](002-its-own-repository.md) | Its own repository, using nothing of Shrooms' code | accepted |
| [003](003-presets-name-what-a-machine-keeps-the-first-fleet.md) | Presets name what a machine keeps; the first fleet | accepted; presets 0.7–5 MB a machine, agent-history 37 MB–1.5 GB |
| [004](004-settings-come-from-the-apps-shrooms-backup-in-base.md) | Settings come from the apps; Shrooms Backup in Basecamp | accepted; core 0.1.1, view 0.1.3 |
| [005](005-shrooms-backup-for-android.md) | Shrooms Backup for Android | accepted; 0.1.2; the Keycard tap is next (ADR-008) |
| [006](006-backups-take-a-bounded-share-of-a-disk.md) | Backups take a bounded share of a disk: 10 GB by default, never more than fits | accepted |
| [007](007-keys-from-the-keycard-set-over-the-mesh.md) | Keys from the Keycard (EIP-1581 export + HKDF), set over the mesh, added beside, labelled | accepted; proteus and the laptop |
| [008](008-separate-apps-separate-workflows.md) | Shrooms, Shrooms Agents and Shrooms Backup are separate apps with separate workflows | accepted |
| [009](009-the-installer-updates.md) | One installer; run again, it updates only what it is given | accepted |
| [010](010-logos-storage-ships-in-the-image.md) | logos-storage ships in the image, built by CI from the pinned commit; nothing an install needs comes from a mesh | accepted |
| [011](011-backups-on-several-meshes.md) | Backups on several meshes: a node per mesh, names qualified by mesh, one limit for the machine | accepted; built |
| [012](012-a-run-shows-its-progress-and-waits-while-it-moves.md) | A run shows its progress, and waits for a mirror as long as it moves | accepted; built |
| [013](013-mirrors-fetch-past-gaps-patched-storage.md) | Mirrors fetch past what they cannot find yet; storage patched for many manifests | accepted; daemon built, storage patches pending |

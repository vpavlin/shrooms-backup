# Shrooms Backup

Backups kept on your own machines, over the [Shrooms](https://github.com/vpavlin/shrooms)
mesh. Each machine picks what to keep (its mesh identity, keys, app data,
folders) and which of your other machines keep copies; nothing leaves your
machines.

- **restic** does the backing up: files cut into chunks, each chunk kept once,
  encrypted, snapshots, retention. A change stores only the change.
- **Logos Storage** keeps restic's objects, on a private network of your own
  nodes over the mesh: the source uploads to its node, each mirror's node
  fetches what it lacks. A run counts only when every mirror holds it.
- **Mirrors keep what a source deletes for 30 days**, so a compromised source
  cannot take its history with it.
- **A key per source**, from your Keycard (the app sets it); a mirror only
  ever holds ciphertext.

Decisions and the measurements behind them: [docs/adr](docs/adr/README.md).

## Install

On a machine that runs Shrooms and is in a mesh:

```sh
curl -fsSLO https://raw.githubusercontent.com/vpavlin/shrooms-backup/main/scripts/install.sh
sudo bash install.sh --mirror --always-on                        # keep copies for others
sudo bash install.sh --source /etc/shrooms --mirrors proteus,scribe   # back this machine up
sudo shrooms-backup key                                          # its key, until the card sets it
```

Then `shrooms-backup status`, `shrooms-backup now`, and
`shrooms-backup restore --from MIRROR --target DIR`.

The daemon (`shrooms-backup daemon`, a root service) runs its own Logos
Storage node on the mesh address, serves its API on that address (TCP 7394)
and on loopback (7395, where restic reaches this machine's repository), and
learns the mesh from the Shrooms daemon's control socket.

## Build and test

`make test` (needs restic for the end-to-end tests: `RESTIC=path` or on
`PATH`); `make build`. No cgo, no other dependencies.

## Licence

Apache-2.0 or MIT, at your option.

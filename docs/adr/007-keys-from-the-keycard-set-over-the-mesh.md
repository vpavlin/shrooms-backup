# 007. Keys from the Keycard, set over the mesh

**Status:** accepted 2026-11-02; proteus and the laptop use card keys. Split out of ADR-001, where it was first recorded as a dated section.

**One card, a key per machine.** `shrooms-backup key --keycard --machine
NAME` has the card export the private key at the EIP-1581 path
m/43'/60'/1581'/1'/INDEX' (the one branch a Keycard lets out, "for
encryption"), the index being the first 31 bits of SHA-256("shrooms-backup "
+ NAME); the machine's key is HKDF-SHA256 of it, labelled with the machine.
It is handed to that machine's daemon over the mesh (`PUT /v1/key`, owners
only). Not BIP85, as ADR-052 planned: Václav's card runs applet 3.1, which has
no EXPORT BIP85 (it answered with no data). The EIP-1581 export works on every
applet, so one scheme serves every card, old or new, with the same seed. Nothing to write down beyond the card's recovery phrase; a machine
reinstalled under its name gets its key back from the card. The path and the
derivation are pinned by tests: changing it would leave every backup unopenable from the
card.

**The card goes where a reader or NFC is**, the laptop or the phone, never
into the machine being backed up (old machines have no USB-C, and it would
be odd anyway). Shrooms Backup pairs with the card itself, in a slot of its
own, and keeps the pairing in `~/.config/shrooms-backup`; the reader code is
copied from Shrooms, not imported, and only a cgo build reaches a reader:
the daemon's build stays cgo-free. Václav, on putting the command into the
`shrooms` CLI first: "we should keep the CLI separate".

**Changing a key keeps every backup.** restic's keys each unlock the same
master key, so a new key is added beside the old one (`restic key add`),
checked against the repository, and used from then on. A key is never
removed through the API, so a lost owner device cannot lock a machine out of
its backups; removing the old one is done on the machine. This does not
re-encrypt anything: somebody holding an old key and an old copy of its key
file (a mirror keeps deleted files 30 days) can still read the data. A
leaked key that matters is answered by a new repository and `restic copy`.

`shrooms-backup key` from stdin no longer echoes, asks twice, and never
writes over an existing key (it showed the passphrase as it was pasted,
2026-11-02).

**Keys are labelled and listed for owners** (2026-11-02). A key from the card
is labelled "Keycard" and the first 8 hex characters of the card's key UID
(`Keycard 6e021332`), which is the same on every card made from one recovery
phrase, so it names the seed that opens the backups. `GET /v1/keys` lists a
source's keys for its owners only (ID, label, when, in use), kept ready by
the daemon since reading them takes seconds and a Basecamp view calls
synchronously; both apps show it in Settings and say when only a passphrase
opens a machine. Whether a machine is secured with a Keycard is not hidden
from its owners and not shown to anybody else; it opens nothing without the
card and its PIN.

**Several cards:** a second card made from the same recovery phrase gives the
same keys and needs no setting up (the recommended way to keep one at home
and carry one); a card with another seed is given to each machine the same
way and added beside, so either opens every backup.

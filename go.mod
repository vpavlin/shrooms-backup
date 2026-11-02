module github.com/vpavlin/shrooms-backup

go 1.26.0

toolchain go1.26.10

// No multipath TCP on our listeners (shrooms ADR-075).
godebug multipathtcp=0

require (
	github.com/keycard-tech/keycard-go/v4 v4.0.1
	golang.org/x/term v0.47.0
)

require (
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/ethereum/go-ethereum v1.17.0 // indirect
	github.com/holiman/uint256 v1.3.2 // indirect
	golang.org/x/crypto v0.44.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)

module github.com/vpavlin/shrooms-backup

go 1.26.0

toolchain go1.26.10

// No multipath TCP on our listeners (shrooms ADR-075).
godebug multipathtcp=0

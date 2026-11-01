package backup

import (
	"fmt"
	"syscall"
)

// GB is the unit of quota_gb: a thousand million bytes, as the apps show
// sizes. It was 1<<30 until 2026-11-01, and a quota of 10 read "11 GB".
const GB = 1_000_000_000

// DefaultQuotaGB is what a machine gives backups unless told otherwise: a
// modest share, not the disk. Was 50 (200 for a mirror, from the
// installer) until 2026-11-01: a machine has other work, and backups
// should never be what fills its disk.
const DefaultQuotaGB = 10

// Disk is the filesystem the node's store is on.
type Disk struct {
	Free  int64 `json:"free"`
	Total int64 `json:"total"`
}

// diskOf is a variable so tests do not depend on the disk they run on.
var diskOf = statDisk

func statDisk(dir string) (Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return Disk{}, err
	}
	return Disk{Free: int64(st.Bavail) * int64(st.Bsize), Total: int64(st.Blocks) * int64(st.Bsize)}, nil
}

// Reserve is what stays free for everything else: a tenth of the disk, but
// no more than 10 GB. A tenth alone left jimmy-crib (20 GB free of 233)
// nothing at all for its own backups' staging.
func (k Disk) Reserve() int64 { return min(k.Total/10, 10*GB) }

// MaxQuota is the most a quota may be on this disk: what is free plus what
// the node holds already, less the reserve.
func (k Disk) MaxQuota(held int64) int64 {
	return max(k.Free+held-k.Reserve(), 0)
}

// MinQuota is the least a node starts with, whatever the disk: below it a
// source cannot stage one run, and a quota of 0 is no quota to the node.
const MinQuota = 1 * GB

// AllowedQuota is the quota a node is started with: the configured one, or
// less when the disk cannot hold it, so a quota written for a bigger disk,
// or before the disk filled with other things, never lets backups fill it.
func AllowedQuota(dir string, gb int64, held int64) (int64, string) {
	want := gb * GB
	k, err := diskOf(dir)
	if err != nil {
		return want, ""
	}
	if m := max(k.MaxQuota(held), MinQuota); want > m {
		return m, fmt.Sprintf("the disk has %d GB free: quota %d GB lowered to %d GB", k.Free/GB, gb, m/GB)
	}
	return want, ""
}

// checkQuota refuses a quota the disk cannot hold, in words for the apps.
func checkQuota(dir string, gb int64, held int64) error {
	k, err := diskOf(dir)
	if err != nil {
		return nil
	}
	if m := k.MaxQuota(held); gb*GB > m {
		return fmt.Errorf("its disk has %d GB free of %d GB; backups may have at most %d GB of it (%d GB stays free)", k.Free/GB, k.Total/GB, m/GB, k.Reserve()/GB)
	}
	return nil
}

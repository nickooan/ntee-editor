package staging

import "golang.org/x/sys/unix"

// cloneTree makes target a copy-on-write clone of source (APFS clonefile:
// a whole folder at once, in about the time a listing takes). It fails on a
// filesystem without clones, or across volumes; the caller copies instead.
func cloneTree(source, target string) error {
	return unix.Clonefile(source, target, unix.CLONE_NOFOLLOW)
}

//go:build !darwin

package staging

import "errors"

// cloneTree has no copy-on-write clone to use here: the caller copies.
func cloneTree(source, target string) error {
	return errors.New("no copy-on-write clone on this system")
}

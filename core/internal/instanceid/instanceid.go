// Package instanceid resolves and validates the per-daemon instance identifier
// used to namespace global resources (git branches, worktree paths, Discord
// titles) so multiple dctl daemons can share one Discord home.
package instanceid

import (
	"fmt"
	"regexp"
)

// idRe is the strict slug accepted as an instanceID: lowercase alnum start,
// then lowercase alnum or '-', total length 1..16. No '_' (so the '__'
// title separator can never appear inside an id), no '/' and no '.'.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)

// Validate reports whether id is a well-formed instanceID slug.
func Validate(id string) bool {
	return idRe.MatchString(id)
}

// Slugify derives a short instanceID from a Discord owner snowflake. It returns
// "u" + the last up-to-8 characters of owner, which keeps the result <=9 chars
// and within the validation regex. An empty owner yields an empty string.
func Slugify(owner string) string {
	if owner == "" {
		return ""
	}
	if len(owner) > 8 {
		owner = owner[len(owner)-8:]
	}
	return "u" + owner
}

// Resolve computes the instanceID from an explicit id (e.g. DCTL_INSTANCE_ID)
// and a fallback owner snowflake (e.g. DCTL_OWNER_ID), per Spec §2:
//  1. explicit, when set, must be a valid slug or Resolve errors;
//  2. otherwise the owner is slugified;
//  3. otherwise "" (legacy, non-namespaced mode).
func Resolve(explicit, owner string) (string, error) {
	if explicit != "" {
		if !Validate(explicit) {
			return "", fmt.Errorf("invalid DCTL_INSTANCE_ID %q: want %s", explicit, idRe.String())
		}
		return explicit, nil
	}
	derived := Slugify(owner)
	if derived != "" && !Validate(derived) {
		return "", fmt.Errorf("invalid DCTL_OWNER_ID %q: derived id %q is not a valid slug", owner, derived)
	}
	return derived, nil
}

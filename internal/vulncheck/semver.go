// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vulncheck

import (
	"sort"

	"golang.org/x/mod/semver"
	isem "golang.org/x/vuln/internal/semver"
	"golang.org/x/vuln/osv"
)

func affectsSemver(a osv.Affects, v string) bool {
	if len(a) == 0 {
		// No ranges implies all versions are affected
		return true
	}
	var semverRangePresent bool
	for _, r := range a {
		if r.Type != osv.TypeSemver {
			continue
		}
		semverRangePresent = true
		if containsSemver(r, v) {
			return true
		}
	}
	// If there were no semver ranges present we
	// assume that all semvers are affected, similarly
	// to how to we assume all semvers are affected
	// if there are no ranges at all.
	return !semverRangePresent
}

// containsSemver checks if semver version v is in the
// range encoded by ar. If ar is not a semver range,
// returns false.
//
// Assumes that
//   - exactly one of Introduced or Fixed fields is set
//   - ranges in ar are not overlapping
//   - beginning of time is encoded with .Introduced="0"
//   - no-fix is not an event, as opposed to being an
//     event where Introduced="" and Fixed=""
func containsSemver(ar osv.AffectsRange, v string) bool {
	if ar.Type != osv.TypeSemver {
		return false
	}
	if len(ar.Events) == 0 {
		return true
	}

	// Strip and then add the semver prefix so we can support bare versions,
	// versions prefixed with 'v', and versions prefixed with 'go'.
	v = isem.CanonicalizeSemverPrefix(v)

	// Sort events by semver versions. Event for beginning
	// of time, if present, always comes first.
	sort.SliceStable(ar.Events, func(i, j int) bool {
		e1 := ar.Events[i]
		v1 := e1.Introduced
		if v1 == "0" {
			// -inf case.
			return true
		}
		if e1.Fixed != "" {
			v1 = e1.Fixed
		}

		e2 := ar.Events[j]
		v2 := e2.Introduced
		if v2 == "0" {
			// -inf case.
			return false
		}
		if e2.Fixed != "" {
			v2 = e2.Fixed
		}

		return semver.Compare(isem.CanonicalizeSemverPrefix(v1), isem.CanonicalizeSemverPrefix(v2)) < 0
	})

	var affected bool
	for _, e := range ar.Events {
		if !affected && e.Introduced != "" {
			affected = e.Introduced == "0" || semver.Compare(v, isem.CanonicalizeSemverPrefix(e.Introduced)) >= 0
		} else if affected && e.Fixed != "" {
			affected = semver.Compare(v, isem.CanonicalizeSemverPrefix(e.Fixed)) < 0
		}
	}

	return affected
}

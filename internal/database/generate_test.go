// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package database

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/vuln/internal/report"
	"golang.org/x/vuln/osv"
)

func TestGenerate(t *testing.T) {
	r := report.Report{
		Module: "example.com/vulnerable/v2",
		AdditionalPackages: []report.Additional{
			{
				Module:  "vanity.host/vulnerable",
				Package: "vanity.host/vulnerable/package",
				Symbols: []string{"b", "A.b"},
				Versions: []report.VersionRange{
					{Fixed: "v2.1.1"},
					{Introduced: "v2.3.4", Fixed: "v2.3.5"},
					{Introduced: "v2.5.0"},
				},
			},
			{
				Module:  "example.com/also-vulnerable",
				Package: "example.com/also-vulnerable/package",
				Symbols: []string{"z"},
				Versions: []report.VersionRange{
					{Fixed: "v2.1.1"},
				},
			},
		},
		Versions: []report.VersionRange{
			{Fixed: "v2.1.1"},
			{Introduced: "v2.3.4", Fixed: "v2.3.5"},
			{Introduced: "v2.5.0"},
		},
		Description: "It's a real bad one, I'll tell you that",
		CVE:         "CVE-0000-0000",
		Credit:      "ignored",
		Symbols:     []string{"A", "B.b"},
		OS:          []string{"windows"},
		Arch:        []string{"arm64"},
		Links: report.Links{
			PR:      "pr",
			Commit:  "commit",
			Context: []string{"issue-a", "issue-b"},
		},
	}

	url := "https://vulns.golang.org/GO-1991-0001.html"
	wantEntry := osv.Entry{
		ID:      "GO-1991-0001",
		Details: "It's a real bad one, I'll tell you that",
		References: []osv.Reference{
			{Type: "FIX", URL: "pr"},
			{Type: "FIX", URL: "commit"},
			{Type: "WEB", URL: "issue-a"},
			{Type: "WEB", URL: "issue-b"},
		},
		Aliases: []string{"CVE-0000-0000"},
		Affected: []osv.Affected{
			{
				Package: osv.Package{
					Name:      "example.com/vulnerable/v2",
					Ecosystem: "Go",
				},
				Ranges: []osv.AffectsRange{
					{
						Type: osv.TypeSemver,
						Events: []osv.RangeEvent{
							{
								Introduced: "0",
							},
							{
								Fixed: "2.1.1",
							},
							{
								Introduced: "2.3.4",
							},
							{
								Fixed: "2.3.5",
							},
							{
								Introduced: "2.5.0",
							},
						},
					},
				},
				DatabaseSpecific: osv.DatabaseSpecific{URL: url},
				EcosystemSpecific: osv.EcosystemSpecific{
					Symbols: []string{"A", "B.b"},
					GOOS:    []string{"windows"},
					GOARCH:  []string{"arm64"},
				},
			},
			{
				Package: osv.Package{
					Name:      "vanity.host/vulnerable/package",
					Ecosystem: "Go",
				},
				Ranges: []osv.AffectsRange{
					{
						Type: osv.TypeSemver,
						Events: []osv.RangeEvent{
							{
								Introduced: "0",
							},
							{
								Fixed: "2.1.1",
							},
							{
								Introduced: "2.3.4",
							},
							{
								Fixed: "2.3.5",
							},
							{
								Introduced: "2.5.0",
							},
						},
					},
				},
				DatabaseSpecific: osv.DatabaseSpecific{URL: url},
				EcosystemSpecific: osv.EcosystemSpecific{
					Symbols: []string{"b", "A.b"},
					GOOS:    []string{"windows"},
					GOARCH:  []string{"arm64"},
				},
			},
			{
				Package: osv.Package{
					Name:      "example.com/also-vulnerable/package",
					Ecosystem: "Go",
				},
				Ranges: []osv.AffectsRange{
					{
						Type: osv.TypeSemver,
						Events: []osv.RangeEvent{
							{
								Introduced: "0",
							},
							{
								Fixed: "2.1.1",
							},
						},
					},
				},
				DatabaseSpecific: osv.DatabaseSpecific{URL: url},
				EcosystemSpecific: osv.EcosystemSpecific{
					Symbols: []string{"z"},
					GOOS:    []string{"windows"},
					GOARCH:  []string{"arm64"},
				},
			},
		},
	}
	wantModules := []string{"example.com/vulnerable/v2", "vanity.host/vulnerable", "example.com/also-vulnerable"}
	sort.Strings(wantModules)

	gotEntry, gotModules := Generate("GO-1991-0001", url, r)
	if diff := cmp.Diff(wantEntry, gotEntry, cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })); diff != "" {
		t.Errorf("Generate returned unexpected entry (-want +got):\n%s", diff)
	}
	sort.Strings(gotModules)
	if !reflect.DeepEqual(gotModules, wantModules) {
		t.Errorf("Generate returned unexpected modules: got %v, want %v", gotModules, wantModules)
	}
}

func TestSemverCanonicalize(t *testing.T) {
	in := []report.VersionRange{
		{
			Introduced: "go1.16",
			Fixed:      "go1.17",
		},
	}
	expected := osv.Affects{
		{
			Type: osv.TypeSemver,
			Events: []osv.RangeEvent{
				{
					Introduced: "1.16.0",
				},
				{
					Fixed: "1.17.0",
				},
			},
		},
	}

	out := generateAffectedRanges(in)
	if !reflect.DeepEqual(out, expected) {
		t.Fatalf("unexpected output: got %#v, want %#v", out, expected)
	}
}

// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.17
// +build go1.17

package worker

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/vuln/srv/internal/cveschema"
)

var usePkgsite = flag.Bool("pkgsite", false, "use pkg.go.dev for tests")

func TestTriageV4CVE(t *testing.T) {
	ctx := context.Background()
	url := getPkgsiteURL(t)

	for _, test := range []struct {
		name string
		in   *cveschema.CVE
		want *triageResult
	}{
		{
			"repo path is unknown Go standard library",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://groups.google.com/forum/#!topic/golang-nuts/1234"},
					},
				},
			},
			&triageResult{
				modulePath: stdlibPath,
				stdlib:     true,
			},
		},
		{
			"pkg.go.dev URL is Go standard library package",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://pkg.go.dev/net/http"},
					},
				},
			},
			&triageResult{
				modulePath: "net/http",
				stdlib:     true,
			},
		},
		{
			"repo path is is valid golang.org module path",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://groups.google.com/forum/#!topic/golang-nuts/1234"},
						{URL: "https://golang.org/x/mod"},
					},
				},
			},
			&triageResult{
				modulePath: "golang.org/x/mod",
			},
		},
		{
			"pkg.go.dev URL is is valid golang.org module path",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://pkg.go.dev/golang.org/x/mod"},
					},
				},
			},
			&triageResult{
				modulePath: "golang.org/x/mod",
			},
		},
		{
			"contains golang.org/pkg URL",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://golang.org/pkg/net/http"},
					},
				},
			},
			&triageResult{
				modulePath: "net/http",
				stdlib:     true,
			},
		},
		{
			"contains github.com but not on pkg.go.dev",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://github.com/something/something/404"},
					},
				},
			},
			nil,
		},
		{
			"contains longer module path",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://bitbucket.org/foo/bar/baz/v2"},
					},
				},
			},
			&triageResult{
				modulePath: "bitbucket.org/foo/bar/baz/v2",
			},
		},
		{
			"repo path is not a module",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://bitbucket.org/foo/bar"},
					},
				},
			},
			nil,
		},
		{
			"contains snyk.io URL containing GOLANG",
			&cveschema.CVE{
				References: cveschema.References{
					Data: []cveschema.Reference{
						{URL: "https://snyk.io/vuln/SNYK-GOLANG-12345"},
					},
				},
			},
			&triageResult{
				modulePath: unknownPath,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.in.DataVersion = "4.0"
			got, err := TriageCVE(ctx, test.in, url)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, got,
				cmp.AllowUnexported(triageResult{}),
				cmpopts.IgnoreFields(triageResult{}, "reason")); diff != "" {
				t.Errorf("mismatch (-want, +got):\n%s", diff)
			}
		})
	}
}

func TestKnownToPkgsite(t *testing.T) {
	ctx := context.Background()

	const validModule = "golang.org/x/mod"
	url := getPkgsiteURL(t)

	for _, test := range []struct {
		in   string
		want bool
	}{
		{validModule, true},
		{"github.com/something/something", false},
	} {
		t.Run(test.in, func(t *testing.T) {
			got, err := knownToPkgsite(ctx, url, test.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("%s: got %t, want %t", test.in, got, test.want)
			}
		})
	}
}

// getPkgsiteURL returns a URL to either a fake server or the real pkg.go.dev,
// depending on the usePkgsite flag.
func getPkgsiteURL(t *testing.T) string {
	if *usePkgsite {
		return pkgsiteURL
	}
	// Start a test server that recognizes anything from golang.org and bitbucket.org/foo/bar/baz.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modulePath := strings.TrimPrefix(r.URL.Path, "/mod/")
		if !strings.HasPrefix(modulePath, "golang.org/") &&
			!strings.HasPrefix(modulePath, "bitbucket.org/foo/bar/baz") {
			http.Error(w, "unknown", http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s.URL
}

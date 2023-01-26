// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/template"

	"golang.org/x/exp/maps"
	"golang.org/x/vuln/exp/govulncheck"
	"golang.org/x/vuln/internal"
	"golang.org/x/vuln/osv"
	"golang.org/x/vuln/vulncheck"
)

func printJSON(r *govulncheck.Result) error {
	b, err := json.MarshalIndent(r, "", "\t")
	if err != nil {
		return err
	}
	os.Stdout.Write(b)
	fmt.Println()
	return nil
}

const (
	labelWidth = 16
	lineLength = 55

	introMessage = `govulncheck is an experimental tool. Share feedback at https://go.dev/s/govulncheck-feedback.`

	detailsMessage = `For details, see https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck.`

	binaryProgressMessage = `Scanning your binary for known vulnerabilities...`
)

func printText(r *govulncheck.Result, verbose, source bool) error {
	return doPrintText(os.Stdout, r, verbose, source)
}

func doPrintText(w io.Writer, r *govulncheck.Result, verbose, source bool) error {
	lineWidth := 80 - labelWidth
	funcMap := template.FuncMap{
		// used in template for counting vulnerabilities
		"inc": func(i int) int {
			return i + 1
		},
		// indent reversed to support template pipelining
		"indent": func(n int, s string) string {
			return indent(s, n)
		},
		"wrap": func(s string) string {
			return wrap(s, lineWidth)
		},
	}

	tmplRes := createTmplResult(r, verbose, source)
	tmpl, err := template.New("govulncheck").Funcs(funcMap).Parse(outputTemplate)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, tmplRes)
}

// createTmplResult transforms govulncheck.Result r into a
// template structure for printing.
func createTmplResult(r *govulncheck.Result, verbose, source bool) tmplResult {
	// unaffected are (imported) OSVs none of
	// which vulnerabilities are called.
	var unaffected []tmplVulnInfo
	var affected []tmplVulnInfo
	for _, v := range r.Vulns {
		if !source || v.IsCalled() {
			affected = append(affected, createTmplVulnInfo(v, verbose, source))
		} else {
			// save arbitrary module info for informational message
			m := v.Modules[0]
			// For stdlib vulnerabilities, we use the path of one the
			// packages (typically, there is only one package). Showing
			// "Module: stdlib" to the user is confusing.
			path := m.Path
			if path == internal.GoStdModulePath {
				path = m.Packages[0].Path
			}
			unaffected = append(unaffected, tmplVulnInfo{
				ID:      v.OSV.ID,
				Details: v.OSV.Details,
				Modules: []tmplModVulnInfo{{
					// We currently do not show module names in the
					// "Informational" section. We hence leave the
					// IsStd and Module fields empty.
					Found:     moduleVersionString(path, m.FoundVersion),
					Fixed:     moduleVersionString(path, m.FixedVersion),
					Platforms: platforms("", v.OSV),
				}},
			})
		}
	}

	return tmplResult{
		Unaffected: unaffected,
		Affected:   affected,
	}
}

// createTmplVulnInfo creates a template vuln info for
// a vulnerability that is called by source code or
// present in the binary.
func createTmplVulnInfo(v *govulncheck.Vuln, verbose, source bool) tmplVulnInfo {
	vInfo := tmplVulnInfo{
		ID:      v.OSV.ID,
		Details: v.OSV.Details,
	}

	// stacks returns call stack info of p as a
	// string depending on verbose and source mode.
	stacks := func(p *govulncheck.Package) string {
		if !source {
			return ""
		}

		if verbose {
			return verboseCallStacks(p.CallStacks)
		}
		return defaultCallStacks(p.CallStacks)
	}

	for _, m := range v.Modules {
		if m.Path == internal.GoStdModulePath {
			// For stdlib vulnerabilities, we pretend each package
			// is effectively a module because showing "Module: stdlib"
			// to the user is confusing. In most cases, stdlib
			// vulnerabilities affect only one package anyhow.
			for _, p := range m.Packages {
				if source && len(p.CallStacks) == 0 {
					// package symbols not exercised, nothing to do here
					continue
				}

				vInfo.Modules = append(vInfo.Modules, tmplModVulnInfo{
					IsStd:     true, // stdlib, so Module field is not needed
					Found:     moduleVersionString(p.Path, m.FoundVersion),
					Fixed:     moduleVersionString(p.Path, m.FixedVersion),
					Platforms: platforms(m.Path, v.OSV),
					Stacks:    stacks(p), // for binary mode, this will be ""
				})
			}
			continue
		}

		// For third-party packages, we create a single output entry for
		// the whole module by merging call stack info of each exercised
		// package (in source mode).
		var moduleStacks []string
		if source {
			for _, p := range m.Packages {
				if len(p.CallStacks) == 0 {
					// package symbols not exercised, nothing to do here
					continue
				}
				moduleStacks = append(moduleStacks, stacks(p))
			}
			if len(moduleStacks) == 0 {
				// Some modules of a vuln have symbols exercised.
				// Skip those that don't.
				continue
			}
		}

		vInfo.Modules = append(vInfo.Modules, tmplModVulnInfo{
			IsStd:     false,
			Module:    m.Path,
			Found:     moduleVersionString(m.Path, m.FoundVersion),
			Fixed:     moduleVersionString(m.Path, m.FixedVersion),
			Platforms: platforms(m.Path, v.OSV),
			Stacks:    strings.Join(moduleStacks, "\n"), // for binary mode, this will be ""
		})
	}
	return vInfo
}

func defaultCallStacks(css []govulncheck.CallStack) string {
	var summaries []string
	for _, cs := range css {
		summaries = append(summaries, cs.Summary)
	}

	// Sort call stack summaries and get rid of duplicates.
	// Note that different call stacks can yield same summaries.
	if len(summaries) > 0 {
		sort.Strings(summaries)
		summaries = compact(summaries)
	}
	var b strings.Builder
	for _, s := range summaries {
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

func verboseCallStacks(css []govulncheck.CallStack) string {
	// Display one full call stack for each vuln.
	i := 1
	var b strings.Builder
	for _, cs := range css {
		b.WriteString(fmt.Sprintf("#%d: for function %s\n", i, cs.Symbol))
		for _, e := range cs.Frames {
			b.WriteString(fmt.Sprintf("  %s\n", e.Name()))
			if pos := internal.AbsRelShorter(e.Pos()); pos != "" {
				b.WriteString(fmt.Sprintf("      %s\n", pos))
			}
		}
		i++
	}
	return b.String()
}

// platforms returns a string describing the GOOS, GOARCH,
// or GOOS/GOARCH pairs that the vuln affects for a particular
// module mod. If it affects all of them, it returns the empty
// string.
//
// When mod is an empty string, returns platform information for
// all modules of e.
func platforms(mod string, e *osv.Entry) string {
	platforms := map[string]bool{}
	for _, a := range e.Affected {
		if mod != "" && a.Package.Name != mod {
			continue
		}
		for _, p := range a.EcosystemSpecific.Imports {
			for _, os := range p.GOOS {
				// In case there are no specific architectures,
				// just list the os entries.
				if len(p.GOARCH) == 0 {
					platforms[os] = true
					continue
				}
				// Otherwise, list all the os+arch combinations.
				for _, arch := range p.GOARCH {
					platforms[os+"/"+arch] = true
				}
			}

			// Cover the case where there are no specific
			// operating systems listed.
			if len(p.GOOS) == 0 {
				for _, arch := range p.GOARCH {
					platforms[arch] = true
				}
			}
		}
	}
	keys := maps.Keys(platforms)
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// compact replaces consecutive runs of equal elements with a single copy.
// This is like the uniq command found on Unix.
// compact modifies the contents of the slice s; it does not create a new slice.
//
// Modified (generics removed) from exp/slices/slices.go.
func compact(s []string) []string {
	if len(s) == 0 {
		return s
	}
	i := 1
	last := s[0]
	for _, v := range s[1:] {
		if v != last {
			s[i] = v
			i++
			last = v
		}
	}
	return s[:i]
}

func moduleVersionString(modulePath, version string) string {
	if version == "" {
		return ""
	}
	return fmt.Sprintf("%s@%s", modulePath, version)
}

// indent returns the output of prefixing n spaces to s at every line break,
// except for empty lines. See TestIndent for examples.
func indent(s string, n int) string {
	b := []byte(s)
	var result []byte
	shouldAppend := true
	prefix := strings.Repeat(" ", n)
	for _, c := range b {
		if shouldAppend && c != '\n' {
			result = append(result, prefix...)
		}
		result = append(result, c)
		shouldAppend = c == '\n'
	}
	return string(result)
}

// sourceProgressMessage returns a string of the form
//
//	"Scanning your code and P packages across M dependent modules for known vulnerabilities..."
//
// P is the number of strictly dependent packages of
// topPkgs and Y is the number of their modules.
func sourceProgressMessage(topPkgs []*vulncheck.Package) string {
	pkgs, mods := depPkgsAndMods(topPkgs)

	pkgsPhrase := fmt.Sprintf("%d package", pkgs)
	if pkgs != 1 {
		pkgsPhrase += "s"
	}

	modsPhrase := fmt.Sprintf("%d dependent module", mods)
	if mods != 1 {
		modsPhrase += "s"
	}

	return fmt.Sprintf("Scanning your code and %s across %s for known vulnerabilities...", pkgsPhrase, modsPhrase)
}

// depPkgsAndMods returns the number of packages that
// topPkgs depend on and the number of their modules.
func depPkgsAndMods(topPkgs []*vulncheck.Package) (int, int) {
	tops := make(map[string]bool)
	depPkgs := make(map[string]bool)
	depMods := make(map[string]bool)

	for _, t := range topPkgs {
		tops[t.PkgPath] = true
	}

	var visit func(*vulncheck.Package, bool)
	visit = func(p *vulncheck.Package, top bool) {
		path := p.PkgPath
		if depPkgs[path] {
			return
		}
		if tops[path] && !top {
			// A top package that is a dependency
			// will not be in depPkgs, so we skip
			// reiterating on it here.
			return
		}

		// We don't count a top-level package as
		// a dependency even when they are used
		// as a dependent package.
		if !tops[path] {
			depPkgs[path] = true
			if p.Module != nil { // no module for stdlib
				depMods[p.Module.Path] = true
			}
		}

		for _, d := range p.Imports {
			visit(d, false)
		}
	}

	for _, t := range topPkgs {
		visit(t, true)
	}

	return len(depPkgs), len(depMods)
}

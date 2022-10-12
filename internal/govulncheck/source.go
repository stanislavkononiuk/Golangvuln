// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package govulncheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/vuln/vulncheck"
)

// A PackageError contains errors from loading a set of packages.
type PackageError struct {
	Errors []packages.Error
}

func (e *PackageError) Error() string {
	var b strings.Builder
	fmt.Fprintln(&b, "Packages contain errors:")
	for _, e := range e.Errors {
		fmt.Fprintln(&b, e)
	}
	return b.String()
}

// loadPackages loads the packages matching patterns using cfg, after setting
// the cfg mode flags that vulncheck needs for analysis.
// If the packages contain errors, a PackageError is returned containing a list of the errors,
// along with the packages themselves.
func loadPackages(cfg LegacyConfig) ([]*vulncheck.Package, error) {
	patterns := cfg.Patterns
	cfg.SourceLoadConfig.Mode |= packages.NeedName | packages.NeedImports | packages.NeedTypes |
		packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps |
		packages.NeedModule

	pkgs, err := packages.Load(cfg.SourceLoadConfig, patterns...)
	vpkgs := vulncheck.Convert(pkgs)
	if err != nil {
		return nil, err
	}
	var perrs []packages.Error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		perrs = append(perrs, p.Errors...)
	})
	if len(perrs) > 0 {
		err = &PackageError{perrs}
	}
	return vpkgs, err
}

// callInfo is information about calls to vulnerable functions.
type callInfo struct {
	// callStacks contains all call stacks to vulnerable functions.
	callStacks map[*vulncheck.Vuln][]vulncheck.CallStack

	// vulnGroups contains vulnerabilities grouped by ID and package.
	vulnGroups [][]*vulncheck.Vuln

	// moduleVersions is a map of module paths to versions.
	moduleVersions map[string]string

	// topPackages contains the top-level packages in the call info.
	topPackages map[string]bool
}

// getCallInfo computes call stacks and related information from a vulncheck.Result.
// It also makes a set of top-level packages from pkgs.
func getCallInfo(r *vulncheck.Result, pkgs []*vulncheck.Package) *callInfo {
	pset := map[string]bool{}
	for _, p := range pkgs {
		pset[p.PkgPath] = true
	}
	cs := vulncheck.CallStacks(r)
	updateInitPositions(cs, pkgs)
	return &callInfo{
		callStacks:     cs,
		vulnGroups:     groupByIDAndPackage(r.Vulns),
		moduleVersions: moduleVersionMap(r.Modules),
		topPackages:    pset,
	}
}

func groupByIDAndPackage(vs []*vulncheck.Vuln) [][]*vulncheck.Vuln {
	groups := map[[2]string][]*vulncheck.Vuln{}
	for _, v := range vs {
		key := [2]string{v.OSV.ID, v.PkgPath}
		groups[key] = append(groups[key], v)
	}

	var res [][]*vulncheck.Vuln
	for _, g := range groups {
		res = append(res, g)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i][0].PkgPath < res[j][0].PkgPath
	})
	return res
}

// moduleVersionMap builds a map from module paths to versions.
func moduleVersionMap(mods []*vulncheck.Module) map[string]string {
	moduleVersions := map[string]string{}
	for _, m := range mods {
		v := m.Version
		if m.Replace != nil {
			v = m.Replace.Version
		}
		moduleVersions[m.Path] = v
	}
	return moduleVersions
}

// updateInitPositions populates non-existing positions of init functions
// and their respective calls in callStacks (see #51575).
func updateInitPositions(callStacks map[*vulncheck.Vuln][]vulncheck.CallStack, pkgs []*vulncheck.Package) {
	pMap := pkgMap(pkgs)
	for _, css := range callStacks {
		for _, cs := range css {
			for i, _ := range cs {
				updateInitPosition(&cs[i], pMap)
				if i != len(cs)-1 {
					updateInitCallPosition(&cs[i], cs[i+1], pMap)
				}
			}
		}
	}
}

// updateInitCallPosition updates the position of a call to init in a stack frame, if
// one already does not exist:
//
//	P1.init -> P2.init: position of call to P2.init is the position of "import P2"
//	statement in P1
//
//	P.init -> P.init#d: P.init is an implicit init. We say it calls the explicit
//	P.init#d at the place of "package P" statement.
func updateInitCallPosition(curr *vulncheck.StackEntry, next vulncheck.StackEntry, pkgs map[string]*vulncheck.Package) {
	call := curr.Call
	if !isInit(next.Function) || (call.Pos != nil && call.Pos.IsValid()) {
		// Skip non-init functions and inits whose call site position is available.
		return
	}

	pkg := pkgs[curr.Function.PkgPath]
	var pos token.Position
	if curr.Function.Name == "init" && curr.Function.PkgPath == next.Function.PkgPath {
		// We have implicit P.init calling P.init#d. Set the call position to
		// be at "package P" statement position.
		pos = packageStatementPos(pkg)
	} else {
		// Choose the beginning of the import statement as the position.
		pos = importStatementPos(pkg, next.Function.PkgPath)
	}

	call.Pos = &pos
}

func importStatementPos(pkg *vulncheck.Package, importPath string) token.Position {
	var importSpec *ast.ImportSpec
spec:
	for _, f := range pkg.Syntax {
		for _, impSpec := range f.Imports {
			// Import spec paths have quotation marks.
			impSpecPath, err := strconv.Unquote(impSpec.Path.Value)
			if err != nil {
				panic(fmt.Sprintf("import specification: package path has no quotation marks: %v", err))
			}
			if impSpecPath == importPath {
				importSpec = impSpec
				break spec
			}
		}
	}

	if importSpec == nil {
		// for sanity, in case of a wild call graph imprecision
		return token.Position{}
	}

	// Choose the beginning of the import statement as the position.
	return pkg.Fset.Position(importSpec.Pos())
}

func packageStatementPos(pkg *vulncheck.Package) token.Position {
	if len(pkg.Syntax) == 0 {
		return token.Position{}
	}
	// Choose beginning of the package statement as the position. Pick
	// the first file since it is as good as any.
	return pkg.Fset.Position(pkg.Syntax[0].Package)
}

// updateInitPosition updates the position of P.init function in a stack frame if one
// is not available. The new position is the position of the "package P" statement.
func updateInitPosition(se *vulncheck.StackEntry, pkgs map[string]*vulncheck.Package) {
	fun := se.Function
	if !isInit(fun) || (fun.Pos != nil && fun.Pos.IsValid()) {
		// Skip non-init functions and inits whose position is available.
		return
	}

	pos := packageStatementPos(pkgs[fun.PkgPath])
	fun.Pos = &pos
}

func isInit(f *vulncheck.FuncNode) bool {
	// A source init function, or anonymous functions used in inits, will
	// be named "init#x" by vulncheck (more precisely, ssa), where x is a
	// positive integer. Implicit inits are named simply "init".
	return f.Name == "init" || strings.HasPrefix(f.Name, "init#")
}

// pkgMap creates a map from package paths to packages for all pkgs
// and their transitive imports.
func pkgMap(pkgs []*vulncheck.Package) map[string]*vulncheck.Package {
	m := make(map[string]*vulncheck.Package)
	var visit func(*vulncheck.Package)
	visit = func(p *vulncheck.Package) {
		if _, ok := m[p.PkgPath]; ok {
			return
		}
		m[p.PkgPath] = p

		for _, i := range p.Imports {
			visit(i)
		}
	}

	for _, p := range pkgs {
		visit(p)
	}
	return m
}

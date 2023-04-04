// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package result contains the JSON output structs for govulncheck.
package govulncheck

import (
	"go/token"
	"time"

	"golang.org/x/vuln/internal/osv"
)

// Mode indicates the display mode that the user specified for running
// govulncheck.
type Mode string

const (
	ModeCompact Mode = "Compact"
	ModeVerbose Mode = "Verbose"
)

// AnalysisType indicates the type of analysis performed by govulncheck.
type AnalysisType string

const (
	AnalysisSource AnalysisType = "Source"
	AnalysisBinary AnalysisType = "Binary"
)

// Message is an entry in the output stream. It will always have exactly one
// field filled in.
type Message struct {
	Config        *Config   `json:"config,omitempty"`
	Progress      *Progress `json:"progress,omitempty"`
	Vulnerability *Vuln     `json:"vulnerability,omitempty"`
}

type Config struct {
	// Name is the name of the tool, for example, govulncheck.
	Name string `json:"name,omitempty"`

	// Version is the version of the tool.
	Version string `json:"govulncheck_version,omitempty"`

	// DataSource is the data source used by the tool, for example,
	// vuln.go.dev.
	DataSource string `json:"data_source,omitempty"`

	// LastModified is the last modified time of the data source.
	LastModified *time.Time `json:"last_modified,omitempty"`

	// GoVersion is the version of Go used for analyzing standard library
	// vulnerabilities.
	GoVersion string `json:"go_version,omitempty"`

	// Analysis is the analysis type.
	Analysis AnalysisType `json:"analysis,omitempty"`

	// Mode controls the information that is printed to the user, either
	// compact or verbose.
	Mode Mode `json:"callstack_mode,omitempty"` // TODO: remove field and verbose mode
}

type Progress struct {
	// Message is the progress message.
	Message string `json:"message,omitempty"`
}

// Result is the result of executing Source or Binary.
type Result struct {
	// Vulns contains all vulnerabilities that are called or imported by
	// the analyzed module.
	Vulns []*Vuln `json:"vulnerabilities,omitempty"`
}

// Vuln represents a single OSV entry.
type Vuln struct {
	// OSV contains all data from the OSV entry for this vulnerability.
	OSV *osv.Entry `json:"osv,omitempty"`

	// Modules contains all of the modules in the OSV entry where a
	// vulnerable package is imported by the target source code or binary.
	//
	// For example, a module M with two packages M/p1 and M/p2, where only p1
	// is vulnerable, will appear in this list if and only if p1 is imported by
	// the target source code or binary.
	Modules []*Module `json:"modules,omitempty"`
}

// Module represents a specific vulnerability relevant to a single module.
type Module struct {
	// Path is the module path of the module containing the vulnerability.
	//
	// Importable packages in the standard library will have the path "stdlib".
	Path string `json:"path,omitempty"`

	// FoundVersion is the module version where the vulnerability was found.
	FoundVersion string `json:"found_version,omitempty"`

	// FixedVersion is the module version where the vulnerability was
	// fixed. If there are multiple fixed versions in the OSV report, this will
	// be the latest fixed version.
	//
	// This is empty if a fix is not available.
	FixedVersion string `json:"fixed_version,omitempty"`

	// Packages contains all the vulnerable packages in OSV entry that are
	// imported by the target source code or binary.
	//
	// For example, given a module M with two packages M/p1 and M/p2, where
	// both p1 and p2 are vulnerable, p1 and p2 will each only appear in this
	// list they are individually imported by the target source code or binary.
	Packages []*Package `json:"packages,omitempty"`
}

// Package is a Go package with known vulnerable symbols.
type Package struct {
	// Path is the import path of the package containing the vulnerability.
	Path string `json:"path"`

	// CallStacks contains a representative call stack for each
	// vulnerable symbol that is called.
	//
	// For vulnerabilities found from binary analysis, only CallStack.Symbol
	// will be provided.
	//
	// For non-affecting vulnerabilities reported from the source mode
	// analysis, this will be empty.
	CallStacks []CallStack `json:"callstacks,omitempty"`
}

// CallStacks contains a representative call stack for a vulnerable
// symbol.
type CallStack struct {
	// Symbol is the name of the detected vulnerable function
	// or method.
	//
	// This follows the naming convention in the OSV report.
	Symbol string `json:"symbol"`

	// Summary is a one-line description of the callstack, used by the
	// default govulncheck mode.
	//
	// Example: module3.main calls github.com/shiyanhui/dht.DHT.Run
	Summary string `json:"summary,omitempty"`

	// Frames contains an entry for each stack in the call stack.
	//
	// Frames are sorted starting from the entry point to the
	// imported vulnerable symbol. The last frame in Frames should match
	// Symbol.
	Frames []*StackFrame `json:"frames,omitempty"`
}

// StackFrame represents a call stack entry.
type StackFrame struct {
	// Package is the import path.
	Package string `json:"package,omitempty"`

	// Function is the function name.
	Function string `json:"function,omitempty"`

	// Receiver is the fully qualified receiver type,
	// if the called symbol is a method.
	//
	// The client can create the final symbol name by
	// prepending Receiver to FuncName.
	Receiver string `json:"receiver,omitempty"`

	// Position describes an arbitrary source position
	// including the file, line, and column location.
	// A Position is valid if the line number is > 0.
	Position token.Position `json:"position,omitempty"`
}

// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command vulnreport provides a tool for creating a YAML vulnerability report for
// x/vulndb.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"strings"

	"os"

	"golang.org/x/vuln/srv/internal/derrors"
	"golang.org/x/vuln/srv/internal/report"
	"gopkg.in/yaml.v2"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: vulnreport [cmd] [filename]\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  create [filename]: creates a new vulnerability YAML report\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  lint [filename]: lints a vulnerability YAML report\n")
		flag.PrintDefaults()
	}

	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(1)
	}

	cmd := flag.Arg(0)
	filename := flag.Arg(1)
	switch cmd {
	case "create":
		if err := create(filename); err != nil {
			log.Fatal(err)
		}
	case "lint":
		if err := lint(filename); err != nil {
			log.Fatal(err)
		}
	default:
		flag.Usage()
		log.Fatalf("unsupported command: %q", cmd)
	}
}

func create(filename string) (err error) {
	defer derrors.Wrap(&err, "create(%q)", filename)
	return os.WriteFile(filename,
		[]byte(`module:
package:
versions:
  - introduced:
  - fixed:
description: |

cve:
credit:
symbols:
  -
published:
links:
  commit:
  pr:
  context:
    -
`), 0644)
}

func lint(filename string) (err error) {
	defer derrors.Wrap(&err, "lint(%q)", filename)
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("ioutil.ReadFile: %v", err)
	}

	var vuln report.Report
	err = yaml.UnmarshalStrict(content, &vuln)
	if err != nil {
		return fmt.Errorf("yaml.UnmarshalStrict: %v", err)
	}

	if lints := vuln.Lint(); len(lints) > 0 {
		return fmt.Errorf("vuln.Lint returned errors:\n\t %s", strings.Join(lints, "\n\t"))
	}
	return nil
}

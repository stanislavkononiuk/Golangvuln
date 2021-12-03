// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package worker

import (
	"testing"

	"github.com/google/safehtml/template"
	"github.com/jba/templatecheck"
)

func TestTemplates(t *testing.T) {
	// Check parsed templates.
	staticPath := template.TrustedSourceFromConstant("static")
	index, err := parseTemplate(staticPath, template.TrustedSourceFromConstant("index.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := templatecheck.CheckSafe(index, indexPage{}); err != nil {
		t.Error(err)
	}
}

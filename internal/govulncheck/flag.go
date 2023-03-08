// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !testmode
// +build !testmode

package govulncheck

import (
	"flag"
)

func addTestFlags(flags *flag.FlagSet, cfg *config) {
}

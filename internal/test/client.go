// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

import (
	"context"

	"golang.org/x/vuln/internal/client"
	"golang.org/x/vuln/internal/osv"
)

// MockClient is an in-memory vulnerability
// database client.
type MockClient struct {
	client.Client
	Ret map[string][]*osv.Entry
}

func (mc *MockClient) ByModule(ctx context.Context, a string) ([]*osv.Entry, error) {
	return mc.Ret[a], nil
}

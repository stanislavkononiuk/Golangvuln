// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.17
// +build go1.17

package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/vuln/internal/cveschema"
	"golang.org/x/vuln/internal/worker/log"
	"golang.org/x/vuln/internal/worker/store"
)

func TestRepoCVEFiles(t *testing.T) {
	repo, err := readTxtarRepo("testdata/basic.txtar", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	commit := headCommit(t, repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repoCVEFiles(repo, commit)
	if err != nil {
		t.Fatal(err)
	}

	want := []repoFile{
		{dirPath: "2020/9xxx", filename: "CVE-2020-9283.json", year: 2020, number: 9283},
		{dirPath: "2021/0xxx", filename: "CVE-2021-0001.json", year: 2021, number: 1},
		{dirPath: "2021/0xxx", filename: "CVE-2021-0010.json", year: 2021, number: 10},
		{dirPath: "2021/1xxx", filename: "CVE-2021-1384.json", year: 2021, number: 1384},
	}

	opt := cmpopts.IgnoreFields(repoFile{}, "treeHash", "blobHash")
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(repoFile{}), opt); diff != "" {
		t.Errorf("mismatch (-want, +got):\n%s", diff)
	}
}

func TestDoUpdate(t *testing.T) {
	ctx := log.WithLineLogger(context.Background())
	repo, err := readTxtarRepo("testdata/basic.txtar", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h, err := headHash(repo)
	if err != nil {
		t.Fatal(err)
	}

	purl := pkgsiteURL(t)
	needsIssue := func(cve *cveschema.CVE) (*triageResult, error) {
		return TriageCVE(ctx, cve, purl)
	}

	ref, err := repo.Reference(plumbing.HEAD, true)
	if err != nil {
		t.Fatal(err)
	}

	commitHash := ref.Hash().String()
	knownVulns := []string{"CVE-2020-9283"}

	paths := []string{
		"2021/0xxx/CVE-2021-0001.json",
		"2021/0xxx/CVE-2021-0010.json",
		"2021/1xxx/CVE-2021-1384.json",
		"2020/9xxx/CVE-2020-9283.json",
	}

	var (
		cves       []*cveschema.CVE
		blobHashes []string
	)
	for _, p := range paths {
		cve, bh := readCVE(t, repo, p)
		cves = append(cves, cve)
		blobHashes = append(blobHashes, bh)
	}
	// CVERecords after the above CVEs are added to an empty DB.
	var rs []*store.CVERecord
	for i := 0; i < len(cves); i++ {
		r := &store.CVERecord{
			ID:         cves[i].ID,
			CVEState:   cves[i].State,
			Path:       paths[i],
			BlobHash:   blobHashes[i],
			CommitHash: commitHash,
		}
		rs = append(rs, r)
	}
	rs[0].TriageState = store.TriageStateNeedsIssue // a public CVE, has a golang.org path
	rs[0].Module = "golang.org/x/mod"
	rs[0].CVE = cves[0]
	rs[1].TriageState = store.TriageStateNoActionNeeded // state is reserved
	rs[2].TriageState = store.TriageStateNoActionNeeded // state is rejected
	rs[3].TriageState = store.TriageStateNoActionNeeded
	rs[3].TriageStateReason = "already in vuln DB"

	// withTriageState returns a copy of r with the TriageState field changed to ts.
	withTriageState := func(r *store.CVERecord, ts store.TriageState) *store.CVERecord {
		c := *r
		c.BlobHash += "x" // if we don't use a different blob hash, no update will happen
		c.CommitHash = "?"
		c.TriageState = ts
		return &c
	}

	for _, test := range []struct {
		name string
		cur  []*store.CVERecord // current state of DB
		want []*store.CVERecord // expected state after update
	}{
		{
			name: "empty",
			cur:  nil,
			want: rs,
		},
		{
			name: "no change",
			cur:  rs,
			want: rs,
		},
		{
			name: "pre-issue changes",
			cur: []*store.CVERecord{
				// NoActionNeeded -> NeedsIssue
				withTriageState(rs[0], store.TriageStateNoActionNeeded),
				// NeedsIssue -> NoActionNeeded
				func() *store.CVERecord {
					r := withTriageState(rs[1], store.TriageStateNeedsIssue)
					r.Module = "something"
					r.CVE = cves[1]
					return r
				}(),
				// NoActionNeeded, triage state stays the same but other fields change.
				withTriageState(rs[2], store.TriageStateNoActionNeeded),
			},
			want: []*store.CVERecord{
				rs[0],
				func() *store.CVERecord {
					c := *rs[1]
					c.Module = ""
					c.CVE = nil
					return &c
				}(),
				rs[2],
				rs[3],
			},
		},
		{
			name: "post-issue changes",
			cur: []*store.CVERecord{
				// IssueCreated -> Updated
				withTriageState(rs[0], store.TriageStateIssueCreated),
				withTriageState(rs[1], store.TriageStateUpdatedSinceIssueCreation),
			},
			want: []*store.CVERecord{
				func() *store.CVERecord {
					c := *rs[0]
					c.TriageState = store.TriageStateUpdatedSinceIssueCreation
					c.TriageStateReason = `CVE changed; affected module = "golang.org/x/mod"`
					return &c
				}(),
				func() *store.CVERecord {
					c := *rs[1]
					c.TriageState = store.TriageStateUpdatedSinceIssueCreation
					c.TriageStateReason = `CVE changed; affected module = ""`
					return &c
				}(),
				rs[2],
				rs[3],
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mstore := store.NewMemStore()
			createCVERecords(t, mstore, test.cur)
			if _, err := newUpdater(repo, h, mstore, knownVulns, needsIssue).update(ctx); err != nil {
				t.Fatal(err)
			}
			got := mstore.CVERecords()
			want := map[string]*store.CVERecord{}
			for _, cr := range test.want {
				want[cr.ID] = cr
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("mismatch (-want, +got):\n%s", diff)
			}
		})
	}
}

func TestGroupFilesByDirectory(t *testing.T) {
	for _, test := range []struct {
		in   []repoFile
		want [][]repoFile
	}{
		{in: nil, want: nil},
		{
			in:   []repoFile{{dirPath: "a"}},
			want: [][]repoFile{{{dirPath: "a"}}},
		},
		{
			in: []repoFile{
				{dirPath: "a", filename: "f1"},
				{dirPath: "a", filename: "f2"},
			},
			want: [][]repoFile{{
				{dirPath: "a", filename: "f1"},
				{dirPath: "a", filename: "f2"},
			}},
		},
		{
			in: []repoFile{
				{dirPath: "a", filename: "f1"},
				{dirPath: "a", filename: "f2"},
				{dirPath: "b", filename: "f1"},
				{dirPath: "c", filename: "f1"},
				{dirPath: "c", filename: "f2"},
			},
			want: [][]repoFile{
				{
					{dirPath: "a", filename: "f1"},
					{dirPath: "a", filename: "f2"},
				},
				{
					{dirPath: "b", filename: "f1"},
				},
				{
					{dirPath: "c", filename: "f1"},
					{dirPath: "c", filename: "f2"},
				},
			},
		},
	} {
		got, err := groupFilesByDirectory(test.in)
		if err != nil {
			t.Fatalf("%v: %v", test.in, err)
		}
		if diff := cmp.Diff(got, test.want, cmp.AllowUnexported(repoFile{})); diff != "" {
			t.Errorf("%v: (-want, +got)\n%s", test.in, diff)
		}
	}

	_, err := groupFilesByDirectory([]repoFile{{dirPath: "a"}, {dirPath: "b"}, {dirPath: "a"}})
	if err == nil {
		t.Error("got nil, want error")
	}
}

func readCVE(t *testing.T, repo *git.Repository, path string) (*cveschema.CVE, string) {
	c := headCommit(t, repo)
	file, err := c.File(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var cve cveschema.CVE
	r, err := file.Reader()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(r).Decode(&cve); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &cve, file.Hash.String()
}

func createCVERecords(t *testing.T, s store.Store, crs []*store.CVERecord) {
	err := s.RunTransaction(context.Background(), func(_ context.Context, tx store.Transaction) error {
		for _, cr := range crs {
			if err := tx.CreateCVERecord(cr); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

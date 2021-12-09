// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package worker

// This file has the public API of the worker, used by cmd/worker as well
// as the server in this package.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"golang.org/x/exp/event"
	"golang.org/x/sync/errgroup"
	vulnc "golang.org/x/vuln/client"
	"golang.org/x/vuln/internal/cveschema"
	"golang.org/x/vuln/internal/derrors"
	"golang.org/x/vuln/internal/gitrepo"
	"golang.org/x/vuln/internal/worker/log"
	"golang.org/x/vuln/internal/worker/store"
)

// UpdateCommit performs an update on the store using the given commit.
// Unless force is true, it checks that the update makes sense before doing it.
func UpdateCommit(ctx context.Context, repoPath, commitHash string, st store.Store, pkgsiteURL string, force bool) (err error) {
	defer derrors.Wrap(&err, "RunCommitUpdate(%q, %q, force=%t)", repoPath, commitHash, force)

	repo, err := gitrepo.CloneOrOpen(repoPath)
	if err != nil {
		return err
	}
	ch := plumbing.NewHash(commitHash)
	if !force {
		if err := checkUpdate(ctx, repo, ch, st); err != nil {
			return err
		}
	}
	knownVulnIDs, err := readVulnDB(ctx)
	if err != nil {
		return err
	}
	u := newUpdater(repo, ch, st, knownVulnIDs, func(cve *cveschema.CVE) (*triageResult, error) {
		return TriageCVE(ctx, cve, pkgsiteURL)
	})
	_, err = u.update(ctx)
	return err
}

// checkUpdate performs sanity checks on a potential update.
// It verifies that there is not an update currently in progress,
// and it makes sure that the update is to a more recent commit.
func checkUpdate(ctx context.Context, repo *git.Repository, commitHash plumbing.Hash, st store.Store) error {
	urs, err := st.ListCommitUpdateRecords(ctx, 1)
	if err != nil {
		return err
	}
	if len(urs) == 0 {
		// No updates, we're good.
		return nil
	}
	lu := urs[0]
	if lu.EndedAt.IsZero() {
		return &CheckUpdateError{
			msg: fmt.Sprintf("latest update started %s ago and has not finished", time.Since(lu.StartedAt)),
		}
	}
	if lu.Error != "" {
		return &CheckUpdateError{
			msg: fmt.Sprintf("latest update finished with error %q", lu.Error),
		}
	}
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return err
	}
	if commit.Committer.When.Before(lu.CommitTime) {
		return &CheckUpdateError{
			msg: fmt.Sprintf("commit %s time %s is before latest update commit %s time %s",
				commitHash, commit.Committer.When.Format(time.RFC3339),
				lu.CommitHash, lu.CommitTime.Format(time.RFC3339)),
		}
	}
	return nil
}

// CheckUpdateError is an error returned from UpdateCommit that can be avoided
// calling UpdateCommit with force set to true.
type CheckUpdateError struct {
	msg string
}

func (c *CheckUpdateError) Error() string {
	return c.msg
}

const vulnDBURL = "https://storage.googleapis.com/go-vulndb"

// readVulnDB returns a list of all CVE IDs in the Go vuln DB.
func readVulnDB(ctx context.Context) ([]string, error) {
	const concurrency = 4

	client, err := vulnc.NewClient([]string{vulnDBURL}, vulnc.Options{})
	if err != nil {
		return nil, err
	}

	goIDs, err := client.ListIDs(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu     sync.Mutex
		cveIDs []string
	)
	sem := make(chan struct{}, concurrency)
	g, ctx := errgroup.WithContext(ctx)
	for _, id := range goIDs {
		id := id
		sem <- struct{}{}
		g.Go(func() error {
			defer func() { <-sem }()
			e, err := client.GetByID(ctx, id)
			if err != nil {
				return err
			}
			// Assume all the aliases are CVE IDs.
			mu.Lock()
			cveIDs = append(cveIDs, e.Aliases...)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return cveIDs, nil
}

func CreateIssues(ctx context.Context, st store.Store, ic IssueClient, limit int) (err error) {
	derrors.Wrap(&err, "CreateIssues(destination: %s)", ic.Destination())

	log.Info(ctx, "CreateIssues starting", event.String("destination", ic.Destination()))
	needsIssue, err := st.ListCVERecordsWithTriageState(ctx, store.TriageStateNeedsIssue)
	if err != nil {
		return err
	}
	numCreated := int64(0)
	for _, r := range needsIssue {
		if limit > 0 && int(numCreated) >= limit {
			break
		}
		if r.IssueReference != "" || !r.IssueCreatedAt.IsZero() {
			log.Error(ctx, "triage state is NeedsIssue but issue field(s) non-zero; skipping",
				event.String("ID", r.ID),
				event.String("IssueReference", r.IssueReference),
				event.Value("IssueCreatedAt", r.IssueCreatedAt))
			continue
		}
		body, err := newBody(r)
		if err != nil {
			return err
		}

		// Create the issue.
		iss := &Issue{
			Title:  fmt.Sprintf("x/vulndb: potential Go vulnerability found from CVE List: %s", r.ID),
			Body:   body,
			Labels: []string{"Needs Triage"},
		}
		num, err := ic.CreateIssue(ctx, iss)
		if err != nil {
			return fmt.Errorf("creating issue for %s: %w", r.ID, err)
		}
		// If we crashed here, we would have filed an issue without recording
		// that fact in the DB. That can lead to duplicate issues, but nothing
		// worse (we won't miss a CVE).
		// TODO(golang/go#49733): look for the issue title to avoid duplications.
		ref := ic.Reference(num)
		log.Info(ctx, "created issue", event.String("CVE", r.ID), event.String("reference", ref))

		// Update the CVERecord in the DB with issue information.
		err = st.RunTransaction(ctx, func(ctx context.Context, tx store.Transaction) error {
			rs, err := tx.GetCVERecords(r.ID, r.ID)
			if err != nil {
				return err
			}
			r := rs[0]
			r.TriageState = store.TriageStateIssueCreated
			r.IssueReference = ref
			r.IssueCreatedAt = time.Now()
			return tx.SetCVERecord(r)
		})
		if err != nil {
			return err
		}
		numCreated++
	}
	log.Info(ctx, "CreateIssues done", event.Int64("limit", int64(limit)), event.Int64("numCreated", numCreated))
	return nil
}

const englishLang = "eng"

func newBody(r *store.CVERecord) (string, error) {
	var b strings.Builder
	var desc string
	if r.CVE != nil {
		for _, d := range r.CVE.Description.Data {
			if d.Lang == englishLang {
				desc = d.Value
			}
		}
	}
	err := issueTemplate.Execute(&b, issueTemplateData{
		Heading: fmt.Sprintf(
			"One or more of the reference URLs in [%s](%s/tree/%s/%s) refers to a Go module.",
			r.ID, gitrepo.CVEListRepoURL, r.CommitHash, r.Path),
		Description: desc,
		CVERecord:   r,
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

type issueTemplateData struct {
	Heading     string
	Description string
	*store.CVERecord
}

var issueTemplate = template.Must(template.New("issue").Parse(`
{{.Heading}}

module: {{.Module}}
package:
stdlib:
versions:
  - introduced:
  - fixed:
description: |
  {{.Description}}

cve: {{.ID}}
credit:
symbols:
  -
published:
links:
  commit:
  pr:
  context:
    -
`))

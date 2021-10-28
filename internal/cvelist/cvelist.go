// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cvelist is used to fetch and parse information from
// https://github.com/CVEProject/cvelist
package cvelist

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/vulndb/internal/cveschema"
	"golang.org/x/vulndb/internal/derrors"
	"golang.org/x/vulndb/internal/report"
)

// Run clones the CVEProject/cvelist repository and compares the files to the
// existing triaged-cve-list.
func Run(triaged map[string]bool) (err error) {
	defer derrors.Wrap(&err, "Run(triaged)")
	log.Printf("Cloning %q...", cvelistRepoURL)
	repo, root, err := cloneRepo(cvelistRepoURL)
	if err != nil {
		return err
	}
	if err := createIssuesToTriage(repo, root, triaged); err != nil {
		return err
	}
	return nil
}

const cvelistRepoURL = "https://github.com/CVEProject/cvelist"

// cloneRepo returns a repo and tree object for the repo at HEAD by
// cloning the repo at repoURL.
func cloneRepo(repoURL string) (repo *git.Repository, root *object.Tree, err error) {
	defer derrors.Wrap(&err, "cloneRepo(%q)", repoURL)
	repo, err = git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
		URL:           repoURL,
		ReferenceName: plumbing.HEAD,
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
	})
	if err != nil {
		return nil, nil, err
	}
	refName := plumbing.HEAD
	ref, err := repo.Reference(refName, true)
	if err != nil {
		return nil, nil, err
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, nil, err
	}
	root, err = repo.TreeObject(commit.TreeHash)
	if err != nil {
		return nil, nil, err
	}
	return repo, root, nil
}

// createIssuesToTriage creates GitHub issues to be triaged by the Go security
// team.
// TODO: Create GitHub issues. At the moment, this just prints the number of
// issues to be created.
func createIssuesToTriage(r *git.Repository, t *object.Tree, triaged map[string]bool) (err error) {
	defer derrors.Wrap(&err, "createIssuesToTriage(r, t, triaged)")
	log.Printf("Finding new Go vulnerabilities from CVE list...")
	cves, issues, err := walkRepo(r, t, "", triaged)
	if err != nil {
		return err
	}
	// TODO: log CVE states in a CVE record.
	// TODO: create GitHub issues.

	var numRefs int
	for _, issue := range issues {
		if issue.AdditionalInfo.Reason == reasonReferenceData {
			numRefs += 1
		}
	}
	log.Printf("Found %d new issues from %d CVEs (%d based on reference data)",
		len(issues), len(cves), numRefs)
	return nil
}

// walkRepo looks at the files in t, recursively, and check if it is a CVE that
// needs to be manually triaged.
func walkRepo(r *git.Repository, t *object.Tree, dirpath string, triaged map[string]bool) (newCVEs map[string]bool, newIssues []*GoVulnIssue, err error) {
	defer derrors.Wrap(&err, "walkRepo(r, t, %q, triaged)", dirpath)
	newCVEs = map[string]bool{}
	for _, e := range t.Entries {
		fp := path.Join(dirpath, e.Name)
		if !strings.HasPrefix(fp, "202") {
			continue
		}
		switch e.Mode {
		case filemode.Dir:
			t2, err := r.TreeObject(e.Hash)
			if err != nil {
				return nil, nil, err
			}
			cves, issues, err := walkRepo(r, t2, fp, triaged)
			if err != nil {
				return nil, nil, err
			}
			for c := range cves {
				newCVEs[c] = true
			}
			newIssues = append(newIssues, issues...)
		default:
			if !strings.HasPrefix(e.Name, "CVE-") {
				continue
			}
			cveID := strings.TrimSuffix(e.Name, ".json")
			if triaged[cveID] {
				continue
			}
			newCVEs[cveID] = true
			c, err := parseCVE(r, e)
			if err != nil {
				return nil, nil, err
			}
			issue, err := cveToIssue(c)
			if err != nil {
				return nil, nil, err
			}
			if issue != nil {
				log.Printf("New CVE to triage: %q (%q)\n", cveID, issue.Report.Module)
				newIssues = append(newIssues, issue)
			}
		}
	}
	return newCVEs, newIssues, nil
}

// parseCVEJSON parses a CVE file following the CVE JSON format:
// https://github.com/CVEProject/automation-working-group/blob/master/cve_json_schema/DRAFT-JSON-file-format-v4.md
func parseCVE(r *git.Repository, e object.TreeEntry) (_ *cveschema.CVE, err error) {
	defer derrors.Wrap(&err, "parseCVE(r, e)")
	blob, err := r.BlobObject(e.Hash)
	if err != nil {
		return nil, fmt.Errorf("r.BlobObject: %v", err)
	}
	src, err := blob.Reader()
	if err != nil {
		return nil, fmt.Errorf("blob.Reader: %v", err)
	}
	defer func() {
		cerr := src.Close()
		if err == nil {
			err = cerr
		}
	}()
	var c cveschema.CVE
	d := json.NewDecoder(src)
	if err := d.Decode(&c); err != nil {
		return nil, fmt.Errorf("d.Decode: %v", err)
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

const goGitHubRepo = "github.com/golang/go"

// cveToIssue creates a GoVulnIssue from a c *cveschema.CVE.
func cveToIssue(c *cveschema.CVE) (_ *GoVulnIssue, err error) {
	defer derrors.Wrap(&err, "cveToIssue(c)")
	if isPendingCVE(c) {
		return nil, nil
	}
	mp, err := modulePathFromCVE(c)
	if err != nil {
		return nil, fmt.Errorf("modulePathFromCVE: %v", err)
	}

	if mp == "" {
		return nil, nil
	}
	// TODO: implement additional checks on description and vendor information.

	var links report.Links
	for _, r := range c.References.ReferenceData {
		if links.Commit == "" && strings.Contains(r.URL, "/commit/") {
			links.Commit = r.URL
		} else if links.PR == "" && strings.Contains(r.URL, "/pull/") {
			links.PR = r.URL
		} else {
			links.Context = append(links.Context, r.URL)
		}
	}

	var cwe string
	for _, pt := range c.Problemtype.ProblemtypeData {
		for _, d := range pt.Description {
			if strings.Contains(d.Value, "CWE") {
				cwe = d.Value
			}
		}
	}
	r := report.Report{
		Module:      mp,
		Links:       links,
		CVE:         c.CVEDataMeta.ID,
		Description: description(c),
	}
	if mp == goGitHubRepo {
		r.Stdlib = true
	}
	info := AdditionalInfo{
		Products: products(c),
		CWE:      cwe,
		Reason:   reasonReferenceData,
	}
	return &GoVulnIssue{Report: r, AdditionalInfo: info}, nil
}

// isPendingCVE reports if the CVE is still waiting on information and not
// ready to be triaged.
func isPendingCVE(c *cveschema.CVE) bool {
	return c.CVEDataMeta.STATE == cveschema.StateReserved
}

var vcsHostsWithThreeElementRepoName = map[string]bool{
	"bitbucket.org": true,
	"gitea.com":     true,
	"gitee.com":     true,
	"github.com":    true,
	"gitlab.com":    true,
	"golang.org":    true,
}

// modulePathFromCVE returns a Go module path for a CVE, if we can determine
// what it is.
func modulePathFromCVE(c *cveschema.CVE) (_ string, err error) {
	defer derrors.Wrap(&err, "modulePathFromCVE(c)")
	for _, r := range c.References.ReferenceData {
		if r.URL == "" {
			continue
		}
		for host := range vcsHostsWithThreeElementRepoName {
			if !strings.Contains(r.URL, host) {
				continue
			}
			refURL, err := url.Parse(r.URL)
			if err != nil {
				return "", fmt.Errorf("url.Parse(%q): %v", r.URL, err)
			}
			u := refURL.Host + refURL.Path
			parts := strings.Split(u, "/")
			if len(parts) < 3 {
				continue
			}
			mod := strings.Join(parts[0:3], "/")
			r, err := http.DefaultClient.Get(fmt.Sprintf("https://pkg.go.dev/%s", mod))
			if err != nil {
				return "", err
			}
			if r.StatusCode == http.StatusOK {
				return mod, nil
			}
		}
	}
	return "", nil
}

const reasonReferenceData = "This CVE was identified as a go vuln because a Go module path was found in reference data."

// GoVulnIssue represents a GitHub issue to be created about a Go
// vulnerability.
type GoVulnIssue struct {
	AdditionalInfo AdditionalInfo
	Report         report.Report
}

// AdditionalInfo contains additional information about the CVE not captured by
// report.Report.
type AdditionalInfo struct {
	CWE      string
	Products []*cveschema.ProductDataItem
	Reason   string
}

func description(c *cveschema.CVE) string {
	var ds []string
	for _, d := range c.Description.DescriptionData {
		ds = append(ds, d.Value)
	}
	return strings.Join(ds, "| \n ")
}

func products(c *cveschema.CVE) []*cveschema.ProductDataItem {
	var pds []*cveschema.ProductDataItem
	for _, v := range c.Affects.Vendor.VendorData {
		for _, pd := range v.Product.ProductData {
			pds = append(pds, &pd)
		}
	}
	return pds
}

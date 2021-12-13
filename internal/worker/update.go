// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/exp/event"
	"golang.org/x/vuln/internal/cveschema"
	"golang.org/x/vuln/internal/derrors"
	"golang.org/x/vuln/internal/worker/log"
	"golang.org/x/vuln/internal/worker/store"
)

// A triageFunc triages a CVE: it decides whether an issue needs to be filed.
// If so, it returns a non-empty string indicating the possibly
// affected module.
type triageFunc func(*cveschema.CVE) (*triageResult, error)

// An updater performs an update operation on the DB.
type updater struct {
	repo           *git.Repository
	commitHash     plumbing.Hash
	st             store.Store
	knownIDs       map[string]bool
	affectedModule triageFunc
}

type updateStats struct {
	numProcessed, numAdded, numModified int
}

// newUpdater creates an updater for updating the store with information from
// the repo commit.
// needsIssue determines whether a CVE needs an issue to be filed for it.
func newUpdater(repo *git.Repository, commitHash plumbing.Hash, st store.Store, knownVulnIDs []string, needsIssue triageFunc) *updater {
	u := &updater{
		repo:           repo,
		commitHash:     commitHash,
		st:             st,
		knownIDs:       map[string]bool{},
		affectedModule: needsIssue,
	}
	for _, k := range knownVulnIDs {
		u.knownIDs[k] = true
	}
	return u
}

// update updates the DB to match the repo at the given commit.
// It also triages new or changed issues.
func (u *updater) update(ctx context.Context) (ur *store.CommitUpdateRecord, err error) {
	// We want the action of reading the old DB record, updating it and
	// writing it back to be atomic. It would be too expensive to do that one
	// record at a time. Ideally we'd process the whole repo commit in one
	// transaction, but Firestore has a limit on how many writes one
	// transaction can do, so the CVE files in the repo are processed in
	// batches, one transaction per batch.
	defer derrors.Wrap(&err, "updater.update(%s)", u.commitHash)

	defer func() {
		if err != nil {
			log.Error(ctx, "update failed", event.Value("error", err))
		} else {
			nProcessed := int64(0)
			if ur != nil {
				nProcessed = int64(ur.NumProcessed)
			}
			log.Info(ctx, "update succeeded", event.Int64("numProcessed", nProcessed))
		}
	}()

	log.Info(ctx, "update starting", event.String("commit", u.commitHash.String()))

	commit, err := u.repo.CommitObject(u.commitHash)
	if err != nil {
		return nil, err
	}

	// Get all the CVE files.
	// It is cheaper to read all the files from the repo and compare
	// them to the DB in bulk, than to walk the repo and process
	// each file individually.
	files, err := repoCVEFiles(u.repo, commit)
	if err != nil {
		return nil, err
	}
	// Process files in the same directory together, so we can easily skip
	// the entire directory if it hasn't changed.
	filesByDir, err := groupFilesByDirectory(files)
	if err != nil {
		return nil, err
	}

	// Create a new CommitUpdateRecord to describe this run of doUpdate.
	ur = &store.CommitUpdateRecord{
		StartedAt:  time.Now(),
		CommitHash: u.commitHash.String(),
		CommitTime: commit.Committer.When,
		NumTotal:   len(files),
	}
	if err := u.st.CreateCommitUpdateRecord(ctx, ur); err != nil {
		return ur, err
	}

	for _, dirFiles := range filesByDir {
		stats, err := u.updateDirectory(ctx, dirFiles)
		// Change the CommitUpdateRecord in the Store to reflect the results of the directory update.
		if err != nil {
			ur.Error = err.Error()
			if err2 := u.st.SetCommitUpdateRecord(ctx, ur); err2 != nil {
				return ur, fmt.Errorf("update failed with %w, could not set update record: %v", err, err2)
			}
		}
		ur.NumProcessed += stats.numProcessed
		ur.NumAdded += stats.numAdded
		ur.NumModified += stats.numModified
		if err := u.st.SetCommitUpdateRecord(ctx, ur); err != nil {
			return ur, err
		}
	}
	ur.EndedAt = time.Now()
	return ur, u.st.SetCommitUpdateRecord(ctx, ur)
}

// Firestore supports a maximum of 500 writes per transaction.
// See https://cloud.google.com/firestore/quotas.
const maxTransactionWrites = 500

func (u *updater) updateDirectory(ctx context.Context, dirFiles []repoFile) (_ updateStats, err error) {
	dirPath := dirFiles[0].dirPath
	dirHash := dirFiles[0].treeHash.String()

	// A non-empty directory hash means that we have fully processed the directory
	// with that hash. If the stored hash matches the current one, we can skip
	// this directory.
	dbHash, err := u.st.GetDirectoryHash(ctx, dirPath)
	if err != nil {
		return updateStats{}, err
	}
	if dirHash == dbHash {
		log.Infof(ctx, "skipping directory %s because the hashes match", dirPath)
		return updateStats{}, nil
	}
	// Set the hash to something that can't match, until we fully process this directory.
	if err := u.st.SetDirectoryHash(ctx, dirPath, "in progress"); err != nil {
		return updateStats{}, err
	}
	// It's okay if we crash now; the directory hashes are just an optimization.
	// At worst we'll redo this directory next time.

	// Update files in batches.

	var stats updateStats
	for i := 0; i < len(dirFiles); i += maxTransactionWrites {
		j := i + maxTransactionWrites
		if j > len(dirFiles) {
			j = len(dirFiles)
		}
		numBatchAdds, numBatchMods, err := u.updateBatch(ctx, dirFiles[i:j])
		if err != nil {
			return updateStats{}, err
		}
		stats.numProcessed += j - i
		// Add in these two numbers here, instead of in the function passed to
		// RunTransaction, because that function may be executed multiple times.
		stats.numAdded += numBatchAdds
		stats.numModified += numBatchMods
	} // end batch loop

	// We're done with this directory, so we can remember its hash.
	if err := u.st.SetDirectoryHash(ctx, dirPath, dirHash); err != nil {
		return updateStats{}, err
	}
	return stats, nil
}

func (u *updater) updateBatch(ctx context.Context, batch []repoFile) (numAdds, numMods int, err error) {
	startID := idFromFilename(batch[0].filename)
	endID := idFromFilename(batch[len(batch)-1].filename)
	defer derrors.Wrap(&err, "updateBatch(%s-%s)", startID, endID)

	log.Debug(ctx, "update transaction started", event.String("startID", startID), event.String("endID", endID))
	err = u.st.RunTransaction(ctx, func(ctx context.Context, tx store.Transaction) error {
		numAdds = 0
		numMods = 0

		// Read information about the existing state in the store that's
		// relevant to this batch. Since the entries are sorted, we can read
		// a range of IDS.
		crs, err := tx.GetCVERecords(startID, endID)
		if err != nil {
			return err
		}
		idToRecord := map[string]*store.CVERecord{}
		for _, cr := range crs {
			idToRecord[cr.ID] = cr
		}
		// Determine what needs to be added and modified.
		for _, f := range batch {
			id := idFromFilename(f.filename)
			old := idToRecord[id]
			if old != nil && old.BlobHash == f.blobHash.String() {
				// No change; do nothing.
				continue
			}
			added, err := u.handleCVE(f, old, tx)
			if err != nil {
				return err
			}
			if added {
				numAdds++
			} else {
				numMods++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	log.Debug(ctx, "update transaction finished",
		event.String("startID", startID),
		event.String("endID", endID),
		event.Int64("adds", int64(numAdds)),
		event.Int64("mods", int64(numMods)))
	return numAdds, numMods, nil
}

// handleCVE determines how to change the store for a single CVE.
// The CVE will definitely be either added, if it's new, or modified, if it's
// already in the DB.
func (u *updater) handleCVE(f repoFile, old *store.CVERecord, tx store.Transaction) (added bool, err error) {
	defer derrors.Wrap(&err, "handleCVE(%s)", f.filename)

	// Read CVE from repo.
	r, err := blobReader(u.repo, f.blobHash)
	if err != nil {
		return false, err
	}
	pathname := path.Join(f.dirPath, f.filename)
	cve := &cveschema.CVE{}
	if err := json.NewDecoder(r).Decode(cve); err != nil {
		return false, err
	}
	var result *triageResult
	if cve.State == cveschema.StatePublic && !u.knownIDs[cve.ID] {
		result, err = u.affectedModule(cve)
		if err != nil {
			return false, err
		}
	}

	// If the CVE is not in the database, add it.
	if old == nil {
		cr := store.NewCVERecord(cve, pathname, f.blobHash.String())
		cr.CommitHash = u.commitHash.String()
		if result != nil {
			cr.TriageState = store.TriageStateNeedsIssue
			cr.Module = result.modulePath
			cr.CVE = cve
		} else {
			cr.TriageState = store.TriageStateNoActionNeeded
			if u.knownIDs[cve.ID] {
				cr.TriageStateReason = "already in vuln DB"
			}
		}
		if err := tx.CreateCVERecord(cr); err != nil {
			return false, err
		}
		return true, nil
	}
	// Change to an existing record.
	mod := *old // copy the old one
	mod.Path = pathname
	mod.BlobHash = f.blobHash.String()
	mod.CVEState = cve.State
	mod.CommitHash = u.commitHash.String()
	switch old.TriageState {
	case store.TriageStateNoActionNeeded:
		if result != nil {
			// Didn't need an issue before, does now.
			mod.TriageState = store.TriageStateNeedsIssue
			mod.Module = result.modulePath
		}
		// Else don't change the triage state, but we still want
		// to update the other changed fields.
	case store.TriageStateNeedsIssue:
		if result == nil {
			// Needed an issue, no longer does.
			mod.TriageState = store.TriageStateNoActionNeeded
			mod.Module = ""
			mod.CVE = nil
		}
		// Else don't change the triage state, but we still want
		// to update the other changed fields.
	case store.TriageStateIssueCreated, store.TriageStateUpdatedSinceIssueCreation:
		// An issue was filed, so a person should revisit this CVE.
		mod.TriageState = store.TriageStateUpdatedSinceIssueCreation
		var mp string
		if result != nil {
			mp = result.modulePath
		}
		mod.TriageStateReason = fmt.Sprintf("CVE changed; affected module = %q", mp)
		// TODO(golang/go#49733): keep a history of the previous states and their commits.
	default:
		return false, fmt.Errorf("unknown TriageState: %q", old.TriageState)
	}
	// If we're here, then mod is a modification to the DB.
	if err := tx.SetCVERecord(&mod); err != nil {
		return false, err
	}
	return false, nil
}

type repoFile struct {
	dirPath  string
	filename string
	treeHash plumbing.Hash
	blobHash plumbing.Hash
	year     int
	number   int
}

// repoCVEFiles returns all the CVE files in the given repo commit, sorted by
// name.
func repoCVEFiles(repo *git.Repository, commit *object.Commit) (_ []repoFile, err error) {
	defer derrors.Wrap(&err, "repoCVEFiles(%s)", commit.Hash)

	root, err := repo.TreeObject(commit.TreeHash)
	if err != nil {
		return nil, fmt.Errorf("TreeObject: %v", err)
	}
	files, err := walkFiles(repo, root, "", nil)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		// Compare the year and the number, as ints. Using the ID directly
		// would put CVE-2014-100009 before CVE-2014-10001.
		if files[i].year != files[j].year {
			return files[i].year < files[j].year
		}
		return files[i].number < files[j].number
	})
	return files, nil
}

// walkFiles collects CVE files from a repo tree.
func walkFiles(repo *git.Repository, tree *object.Tree, dirpath string, files []repoFile) ([]repoFile, error) {
	for _, e := range tree.Entries {
		if e.Mode == filemode.Dir {
			dir, err := repo.TreeObject(e.Hash)
			if err != nil {
				return nil, err
			}
			files, err = walkFiles(repo, dir, path.Join(dirpath, e.Name), files)
			if err != nil {
				return nil, err
			}
		} else if isCVEFilename(e.Name) {
			// e.Name is CVE-YEAR-NUMBER.json
			year, err := strconv.Atoi(e.Name[4:8])
			if err != nil {
				return nil, err
			}
			number, err := strconv.Atoi(e.Name[9 : len(e.Name)-5])
			if err != nil {
				return nil, err
			}
			files = append(files, repoFile{
				dirPath:  dirpath,
				filename: e.Name,
				treeHash: tree.Hash,
				blobHash: e.Hash,
				year:     year,
				number:   number,
			})
		}
	}
	return files, nil
}

// Collect files by directory, verifying that directories are contiguous in
// the list of files. Our directory hash optimization depends on that.
func groupFilesByDirectory(files []repoFile) ([][]repoFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	var (
		result [][]repoFile
		curDir []repoFile
	)
	for _, f := range files {
		if len(curDir) > 0 && f.dirPath != curDir[0].dirPath {
			result = append(result, curDir)
			curDir = nil
		}
		curDir = append(curDir, f)
	}
	if len(curDir) > 0 {
		result = append(result, curDir)
	}
	seen := map[string]bool{}
	for _, dir := range result {
		if seen[dir[0].dirPath] {
			return nil, fmt.Errorf("directory %s is not contiguous in the sorted list of files", dir[0].dirPath)
		}
		seen[dir[0].dirPath] = true
	}
	return result, nil
}

// blobReader returns a reader to the blob with the given hash.
func blobReader(repo *git.Repository, hash plumbing.Hash) (io.Reader, error) {
	blob, err := repo.BlobObject(hash)
	if err != nil {
		return nil, err
	}
	return blob.Reader()
}

// idFromFilename extracts the CVE ID from its filename.
func idFromFilename(name string) string {
	return strings.TrimSuffix(path.Base(name), path.Ext(name))
}

// isCVEFilename reports whether name is the basename of a CVE file.
func isCVEFilename(name string) bool {
	return strings.HasPrefix(name, "CVE-") && path.Ext(name) == ".json"
}

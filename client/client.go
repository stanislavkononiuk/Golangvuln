// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package client provides an interface for accessing vulnerability
// databases, via either HTTP or local filesystem access.
//
// The expected database layout is the same for both HTTP and local
// databases. The database  index is located at the root of the
// database, and contains a list of all of the vulnerable modules
// documented in the databse and the time the most recent vulnerability
// was added. The index file is called indx.json, and has the
// following format:
//
//   map[string]time.Time (osv.DBIndex)
//
// Each vulnerable module is represented by an individual JSON file
// which contains all of the vulnerabilities in that module. The path
// for each module file is simply the import path of the module,
// i.e. vulnerabilities in golang.org/x/crypto are contained in the
// golang.org/x/crypto.json file. The per-module JSON files have
// the following format:
//
//   []osv.Entry
//
// A single client.Client can be used to access multiple vulnerability
// databases. When looking up vulnerable module each database is
// consulted, and results are merged together.
//
// TODO: allow filtering private module, possibly at a database level?
// (e.g. I may want to use multiple databases, but only lookup a specific
// module in a subset of them)
package client

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/vulndb/osv"
)

type source interface {
	Get([]string) ([]*osv.Entry, error)
	Index() (osv.DBIndex, error)
}

type localSource struct {
	dir string
}

func (ls *localSource) Get(modules []string) ([]*osv.Entry, error) {
	var entries []*osv.Entry
	for _, p := range modules {
		content, err := ioutil.ReadFile(filepath.Join(ls.dir, p+".json"))
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		var e []*osv.Entry
		if err = json.Unmarshal(content, &e); err != nil {
			return nil, err
		}
		entries = append(entries, e...)
	}
	return entries, nil
}

func (ls *localSource) Index() (osv.DBIndex, error) {
	var index osv.DBIndex
	b, err := ioutil.ReadFile(filepath.Join(ls.dir, "index.json"))
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &index); err != nil {
		return nil, err
	}
	return index, nil
}

type httpSource struct {
	url    string // the base URI of the source (without trailing "/"). e.g. https://vuln.golang.org
	c      *http.Client
	cache  Cache
	dbName string
}

func (hs *httpSource) Index() (osv.DBIndex, error) {
	var cachedIndex osv.DBIndex
	var cachedIndexRetrieved *time.Time

	if hs.cache != nil {
		cachedIndex, retrieved, err := hs.cache.ReadIndex(hs.dbName)
		if err != nil {
			return nil, err
		}

		if cachedIndex != nil {
			if time.Since(retrieved) < time.Hour*2 {
				return cachedIndex, nil
			}

			cachedIndexRetrieved = &retrieved
		}
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/index.json", hs.url), nil)
	if err != nil {
		return nil, err
	}
	if cachedIndexRetrieved != nil {
		req.Header.Add("If-Modified-Since", cachedIndexRetrieved.Format(http.TimeFormat))
	}
	resp, err := hs.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if cachedIndexRetrieved != nil && resp.StatusCode == http.StatusNotModified {
		return cachedIndex, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var index osv.DBIndex
	if err = json.Unmarshal(b, &index); err != nil {
		return nil, err
	}

	if hs.cache != nil {
		if err = hs.cache.WriteIndex(hs.dbName, index, time.Now()); err != nil {
			return nil, err
		}
	}

	return index, nil
}

func (hs *httpSource) Get(modules []string) ([]*osv.Entry, error) {
	var entries []*osv.Entry

	index, err := hs.Index()
	if err != nil {
		return nil, err
	}

	var stillNeed []string
	for _, p := range modules {
		lastModified, present := index[p]
		if !present {
			continue
		}
		if hs.cache != nil {
			if cached, err := hs.cache.ReadEntries(hs.dbName, p); err != nil {
				return nil, err
			} else if len(cached) != 0 {
				var stale bool
				for _, c := range cached {
					if c.Modified.Before(lastModified) {
						stale = true
						break
					}
				}
				if !stale {
					entries = append(entries, cached...)
					continue
				}
			}
		}
		stillNeed = append(stillNeed, p)
	}

	for _, p := range stillNeed {
		resp, err := hs.c.Get(fmt.Sprintf("%s/%s.json", hs.url, p))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		// might want this to be a LimitedReader
		content, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var e []*osv.Entry
		if err = json.Unmarshal(content, &e); err != nil {
			return nil, err
		}
		// TODO: we may want to check that the returned entries actually match
		// the module we asked about, so that the cache cannot be poisoned
		entries = append(entries, e...)

		if hs.cache != nil {
			if err := hs.cache.WriteEntries(hs.dbName, p, e); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}

type Client struct {
	sources []source
}

type Options struct {
	HTTPClient *http.Client
	HTTPCache  Cache
}

func NewClient(sources []string, opts Options) (*Client, error) {
	c := &Client{}
	for _, uri := range sources {
		uri = strings.TrimRight(uri, "/")
		// should parse the URI out here instead of in there
		switch {
		case strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://"):
			hs := &httpSource{url: uri}
			url, err := url.Parse(uri)
			if err != nil {
				return nil, err
			}
			hs.dbName = url.Hostname()
			if opts.HTTPCache != nil {
				hs.cache = opts.HTTPCache
			}
			if opts.HTTPClient != nil {
				hs.c = opts.HTTPClient
			} else {
				hs.c = new(http.Client)
			}
			c.sources = append(c.sources, hs)
		case strings.HasPrefix(uri, "file://"):
			c.sources = append(c.sources, &localSource{dir: strings.TrimPrefix(uri, "file://")})
		default:
			return nil, fmt.Errorf("source %q has unsupported scheme", uri)
		}
	}
	return c, nil
}

func (c *Client) Get(modules []string) ([]*osv.Entry, error) {
	var entries []*osv.Entry
	// probably should be parallelized
	for _, s := range c.sources {
		e, err := s.Get(modules)
		if err != nil {
			return nil, err // be failure tolerant?
		}
		entries = append(entries, e...)
	}
	return entries, nil
}

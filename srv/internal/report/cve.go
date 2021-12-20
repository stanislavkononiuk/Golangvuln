// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package report

import (
	"errors"
	"fmt"
	"io/ioutil"
	"strings"

	"golang.org/x/vuln/srv/internal/cveschema"
	"golang.org/x/vuln/srv/internal/derrors"
	"gopkg.in/yaml.v2"
)

func ToCVE(reportPath string) (_ *cveschema.CVE, err error) {
	defer derrors.Wrap(&err, "report.ToCVE(%q)", reportPath)

	b, err := ioutil.ReadFile(reportPath)
	if err != nil {
		return nil, fmt.Errorf("ioutil.ReadFile(%q): %v", reportPath, err)
	}

	var r Report
	if err = yaml.UnmarshalStrict(b, &r); err != nil {
		return nil, fmt.Errorf("yaml.Unmarshal:: %v", err)
	}

	if r.CVE != "" || len(r.CVEs) > 0 {
		return nil, errors.New("report has CVE ID is wrong section (should be in cve_metadata for self-issued CVEs)")
	}
	if r.CVEMetadata == nil {
		return nil, errors.New("report missing cve_metadata section")
	}
	if r.CVEMetadata.ID == "" {
		return nil, errors.New("report missing CVE ID")
	}

	c := &cveschema.CVE{
		DataType:    "CVE",
		DataFormat:  "MITRE",
		DataVersion: "4.0",
		Metadata: cveschema.Metadata{
			ID:       r.CVEMetadata.ID,
			Assigner: "security@golang.org",
			State:    cveschema.StatePublic,
		},

		Description: cveschema.Description{
			Data: []cveschema.LangString{
				{
					Lang:  "eng",
					Value: strings.TrimSuffix(r.CVEMetadata.Description, "\n"),
				},
			},
		},

		ProblemType: cveschema.ProblemType{
			Data: []cveschema.ProblemTypeDataItem{
				{
					Description: []cveschema.LangString{
						{
							Lang:  "eng",
							Value: r.CVEMetadata.CWE,
						},
					},
				},
			},
		},

		Affects: cveschema.Affects{
			Vendor: cveschema.Vendor{
				Data: []cveschema.VendorDataItem{
					{
						VendorName: "n/a", // ???
						Product: cveschema.Product{
							Data: []cveschema.ProductDataItem{
								{
									ProductName: r.Package,
									Version:     versionToVersion(r.Versions),
								},
							},
						},
					},
				},
			},
		},
	}

	for _, additional := range r.AdditionalPackages {
		c.Affects.Vendor.Data = append(c.Affects.Vendor.Data, cveschema.VendorDataItem{
			VendorName: "n/a",
			Product: cveschema.Product{
				Data: []cveschema.ProductDataItem{
					{
						ProductName: additional.Package,
						Version:     versionToVersion(additional.Versions),
					},
				},
			},
		})
	}

	if r.Links.PR != "" {
		c.References.Data = append(c.References.Data, cveschema.Reference{URL: r.Links.PR})
	}
	if r.Links.Commit != "" {
		c.References.Data = append(c.References.Data, cveschema.Reference{URL: r.Links.Commit})
	}
	for _, url := range r.Links.Context {
		c.References.Data = append(c.References.Data, cveschema.Reference{URL: url})
	}

	return c, nil
}

func versionToVersion(versions []VersionRange) cveschema.VersionData {
	vd := cveschema.VersionData{}
	for _, vr := range versions {
		if vr.Introduced != "" {
			vd.Data = append(vd.Data, cveschema.VersionDataItem{
				VersionValue:    vr.Introduced,
				VersionAffected: ">=",
			})
		}
		if vr.Fixed != "" {
			vd.Data = append(vd.Data, cveschema.VersionDataItem{
				VersionValue:    vr.Fixed,
				VersionAffected: "<",
			})
		}
	}
	return vd
}

/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/gosuri/uitable"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"helm.sh/helm/v3/cmd/helm/search"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/helmpath"
	"helm.sh/helm/v3/pkg/repo"
)

const searchRepoDesc = `
Search reads through all of the repositories configured on the system, and
looks for matches. Search of these repositories uses the metadata stored on
the system.

Repositories are managed with 'helm repo' commands.
`

// searchMaxScore suggests that any score higher than this is not considered a match.
const searchMaxScore = 25

type searchRepoOptions struct {
	versions     bool
	regexp       bool
	version      string
	maxColWidth  uint
	repoFile     string
	repoCacheDir string
	outputFormat string
}

func newSearchRepoCmd(out io.Writer) *cobra.Command {
	o := &searchRepoOptions{}

	cmd := &cobra.Command{
		Use:   "repo [keyword]",
		Short: "search repositories for a keyword in charts",
		Long:  searchRepoDesc,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.repoFile = settings.RepositoryConfig
			o.repoCacheDir = settings.RepositoryCache
			return o.run(out, args)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&o.regexp, "regexp", "r", false, "use regular expressions for searching repositories you have added")
	f.BoolVarP(&o.versions, "versions", "l", false, "show the long listing, with each version of each chart on its own line, for repositories you have added")
	f.StringVar(&o.version, "version", "", "search using semantic versioning constraints on repositories you have added")
	f.UintVar(&o.maxColWidth, "max-col-width", 50, "maximum column width for output table")
	bindOutputFlag(cmd, &o.outputFormat)

	return cmd
}

func (o *searchRepoOptions) run(out io.Writer, args []string) error {
	// validate the output format first so we don't waste time running a
	// request that we'll throw away
	outfmt, err := action.ParseOutputFormat(o.outputFormat)
	if err != nil {
		return err
	}

	index, err := o.buildIndex(out)
	if err != nil {
		return err
	}

	var res []*search.Result
	if len(args) == 0 {
		res = index.All()
	} else {
		q := strings.Join(args, " ")
		res, err = index.Search(q, searchMaxScore, o.regexp)
		if err != nil {
			return err
		}
	}

	search.SortScore(res)
	data, err := o.applyConstraint(res)
	if err != nil {
		return err
	}

	return outfmt.Write(out, &repoSearchWriter{data, o.maxColWidth})
}

func (o *searchRepoOptions) applyConstraint(res []*search.Result) ([]*search.Result, error) {
	if len(o.version) == 0 {
		return res, nil
	}

	constraint, err := semver.NewConstraint(o.version)
	if err != nil {
		return res, errors.Wrap(err, "an invalid version/constraint format")
	}

	data := res[:0]
	foundNames := map[string]bool{}
	for _, r := range res {
		if _, found := foundNames[r.Name]; found {
			continue
		}
		v, err := semver.NewVersion(r.Chart.Version)
		if err != nil || constraint.Check(v) {
			data = append(data, r)
			if !o.versions {
				foundNames[r.Name] = true // If user hasn't requested all versions, only show the latest that matches
			}
		}
	}

	return data, nil
}

func (o *searchRepoOptions) buildIndex(out io.Writer) (*search.Index, error) {
	// Load the repositories.yaml
	rf, err := repo.LoadFile(o.repoFile)
	if isNotExist(err) || len(rf.Repositories) == 0 {
		return nil, errors.New("no repositories configured")
	}

	i := search.NewIndex()
	for _, re := range rf.Repositories {
		n := re.Name
		f := filepath.Join(o.repoCacheDir, helmpath.CacheIndexFile(n))
		ind, err := repo.LoadIndexFile(f)
		if err != nil {
			// TODO should print to stderr
			fmt.Fprintf(out, "WARNING: Repo %q is corrupt or missing. Try 'helm repo update'.", n)
			continue
		}

		i.AddRepo(n, ind, o.versions || len(o.version) > 0)
	}
	return i, nil
}

type repoChartElement struct {
	Name        string
	Version     string
	AppVersion  string
	Description string
}

type repoSearchWriter struct {
	results     []*search.Result
	columnWidth uint
}

func (r *repoSearchWriter) WriteTable(out io.Writer) error {
	if len(r.results) == 0 {
		_, err := out.Write([]byte("No results found\n"))
		if err != nil {
			return fmt.Errorf("unable to write results: %s", err)
		}
		return nil
	}
	table := uitable.New()
	table.MaxColWidth = r.columnWidth
	table.AddRow("NAME", "CHART VERSION", "APP VERSION", "DESCRIPTION")
	for _, r := range r.results {
		table.AddRow(r.Name, r.Chart.Version, r.Chart.AppVersion, r.Chart.Description)
	}
	return action.EncodeTable(out, table)
}

func (r *repoSearchWriter) WriteJSON(out io.Writer) error {
	return r.encodeByFormat(out, action.JSON)
}

func (r *repoSearchWriter) WriteYAML(out io.Writer) error {
	return r.encodeByFormat(out, action.YAML)
}

func (r *repoSearchWriter) encodeByFormat(out io.Writer, format action.OutputFormat) error {
	// Initialize the array so no results returns an empty array instead of null
	chartList := make([]repoChartElement, 0, len(r.results))

	for _, r := range r.results {
		chartList = append(chartList, repoChartElement{r.Name, r.Chart.Version, r.Chart.AppVersion, r.Chart.Description})
	}

	switch format {
	case action.JSON:
		return action.EncodeJSON(out, chartList)
	case action.YAML:
		return action.EncodeYAML(out, chartList)
	}

	// Because this is a non-exported function and only called internally by
	// WriteJSON and WriteYAML, we shouldn't get invalid types
	return nil
}
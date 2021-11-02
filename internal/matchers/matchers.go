/*
Copyright 2021 The Kubernetes Authors.

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

package matchers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/onsi/gomega/format"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
)

// This code is adappted from the mergePatch code at controllers/topology/internal/mergepatch pkg.

// These package variables hold pre-created commonly used options that can be used to reduce the manual work involved in
// identifying the paths that need to be compared for testing equality between objects.
var (
	// IgnoreAutogeneratedMetadata contains the paths for all the metadata fields that are commonly set by the
	// client and APIServer. This is used as a MatchOption for situations when only user-provided metadata is relevant.
	IgnoreAutogeneratedMetadata = IgnorePaths{
		{"metadata", "uid"},
		{"metadata", "generation"},
		{"metadata", "creationTimestamp"},
		{"metadata", "resourceVersion"},
		{"metadata", "managedFields"},
		{"metadata", "deletionGracePeriodSeconds"},
		{"metadata", "deletionTimestamp"},
		{"metadata", "selfLink"},
		{"metadata", "generateName"},
	}
)

// Matcher is a Gomega matcher used to establish equality between two Kubernetes runtime.Objects.
type Matcher struct {
	// original holds the object that will be used to Match.
	original runtime.Object

	// diff contains the delta between the two compared objects.
	diff []byte

	// options holds the options that identify what should and should not be matched.
	options *MatchOptions
}

// EqualObject returns a Matcher for the passed Kubernetes runtime.Object with the passed Options. This function can be
// used as a Gomega Matcher in Gomega Assertions.
func EqualObject(original runtime.Object, opts ...MatchOption) *Matcher {
	matchOptions := &MatchOptions{}
	matchOptions = matchOptions.ApplyOptions(opts)

	// set the allowPaths to '*' by default to not exclude any paths from the comparison.
	if len(matchOptions.allowPaths) == 0 {
		matchOptions.allowPaths = [][]string{{"*"}}
	}
	return &Matcher{
		options:  matchOptions,
		original: original,
	}
}

// Match compares the current object to the passed object and returns true if the objects are the same according to
// the Matcher and MatchOptions.
func (m *Matcher) Match(actual interface{}) (success bool, err error) {
	// Nil checks required first here for:
	//     1) Nil equality which returns true
	//     2) One object nil which returns an error
	actualIsNil := reflect.ValueOf(actual).IsNil()
	originalIsNil := reflect.ValueOf(m.original).IsNil()

	if actualIsNil && originalIsNil {
		return true, nil
	}
	if actualIsNil || originalIsNil {
		return false, fmt.Errorf("can not compare an object with a nil. original %v , actual %v", m.original, actual)
	}

	// Calculate diff returns a json diff between the two objects.
	m.diff, err = m.calculateDiff(actual)
	if err != nil {
		return false, err
	}
	return bytes.Equal(m.diff, []byte("{}")), nil
}

// FailureMessage returns a message comparing the full objects after an unexpected failure to match has occurred.
func (m *Matcher) FailureMessage(actual interface{}) (message string) {
	return fmt.Sprintf("the following fields were expected to match but did not:\n%s\n%s", string(m.diff),
		format.Message(actual, "expected to match", m.original))
}

// NegatedFailureMessage returns a string comparing the full objects after an unexpected match has occurred.
func (m *Matcher) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message("the following fields were not expected to match \n%s\n%s", string(m.diff),
		format.Message(actual, "expected to match", m.original))
}

// calculateDiff applies the MatchOptions and identifies the diff between the Matcher object and the actual object.
func (m *Matcher) calculateDiff(actual interface{}) ([]byte, error) {
	// Convert the original and actual objects to json.
	originalJSON, err := json.Marshal(m.original)
	if err != nil {
		return nil, err
	}

	actualJSON, err := json.Marshal(actual)
	if err != nil {
		return nil, err
	}

	// Use a mergePatch to produce a diff between the two objects.
	diff, err := jsonpatch.CreateMergePatch(originalJSON, actualJSON)
	if err != nil {
		return nil, err
	}

	// Filter the diff according to the rules attached to the Matcher.
	diff, err = filterDiff(diff, m.options.allowPaths, m.options.ignorePaths)
	if err != nil {
		return nil, err
	}
	return diff, nil
}

// MatchOption describes an Option that can be applied to a Matcher.
type MatchOption interface {
	// ApplyToMatcher applies this configuration to the given MatchOption.
	ApplyToMatcher(options *MatchOptions)
}

// MatchOptions holds the available types of MatchOptions that can be applied to a Matcher.
type MatchOptions struct {
	ignorePaths [][]string
	allowPaths  [][]string
}

// ApplyOptions adds the passed MatchOptions to the MatchOptions struct.
func (o *MatchOptions) ApplyOptions(opts []MatchOption) *MatchOptions {
	for _, opt := range opts {
		opt.ApplyToMatcher(o)
	}
	return o
}

// IgnorePaths instructs the Matcher to ignore given paths when computing a diff.
type IgnorePaths [][]string

// ApplyToMatcher applies this configuration to the given MatchOptions.
func (i IgnorePaths) ApplyToMatcher(opts *MatchOptions) {
	opts.ignorePaths = append(opts.ignorePaths, i...)
}

// AllowPaths instructs the Matcher to restrict its diff to the given paths. If empty the Matcher will look at all paths.
type AllowPaths [][]string

// ApplyToMatcher applies this configuration to the given MatchOptions.
func (i AllowPaths) ApplyToMatcher(opts *MatchOptions) {
	opts.allowPaths = append(opts.allowPaths, i...)
}

// filterDiff limits the diff to allowPaths if given and excludes ignorePaths if given. It returns the altered diff.
func filterDiff(diff []byte, allowPaths, ignorePaths [][]string) ([]byte, error) {
	// converts the diff into a Map
	diffMap := make(map[string]interface{})
	err := json.Unmarshal(diff, &diffMap)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal merge diff")
	}

	// Removes from diffs everything not in the allowpaths.
	filterDiffMap(diffMap, allowPaths)

	// Removes from diffs everything in the ignore paths.
	for _, path := range ignorePaths {
		removePath(diffMap, path)
	}

	// Converts Map back into the diff.
	diff, err = json.Marshal(&diffMap)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal merge diff")
	}
	return diff, nil
}

// filterDiffMap limits the diffMap to those paths allowed by the MatchOptions.
func filterDiffMap(diffMap map[string]interface{}, allowPaths [][]string) {
	// if the allowPaths only contains "*" return the full diffmap.
	if len(allowPaths) == 1 && allowPaths[0][0] == "*" {
		return
	}

	// Loop through the entries in the map.
	for k, m := range diffMap {
		// Check if item is in the allowPaths.
		allowed := false
		for _, path := range allowPaths {
			if k == path[0] {
				allowed = true
				break
			}
		}

		if !allowed {
			delete(diffMap, k)
			continue
		}

		nestedMap, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		nestedPaths := make([][]string, 0)
		for _, path := range allowPaths {
			if k == path[0] && len(path) > 1 {
				nestedPaths = append(nestedPaths, path[1:])
			}
		}
		if len(nestedPaths) == 0 {
			continue
		}
		filterDiffMap(nestedMap, nestedPaths)

		if len(nestedMap) == 0 {
			delete(diffMap, k)
		}
	}
}

// removePath excludes any path passed in the ignorePath MatchOption from the diff.
func removePath(diffMap map[string]interface{}, path []string) {
	switch len(path) {
	case 0:
		// If path is empty, no-op.
		return
	case 1:
		// If we are at the end of a path, remove the corresponding entry.
		delete(diffMap, path[0])
	default:
		// If in the middle of a path, go into the nested map.
		nestedMap, ok := diffMap[path[0]].(map[string]interface{})
		if !ok {
			return
		}
		removePath(nestedMap, path[1:])

		// Ensure we are not leaving empty maps around.
		if len(nestedMap) == 0 {
			delete(diffMap, path[0])
		}
	}
}

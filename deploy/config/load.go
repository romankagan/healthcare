/*
 * Copyright 2019 Google LLC.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/imdario/mergo"
	"github.com/mitchellh/go-homedir"
)

// NormalizePath normalizes paths specified through a local run or Bazel invocation.
func NormalizePath(path string) (string, error) {
	path, err := homedir.Expand(path)
	if err != nil {
		return "", err
	}
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "gs://") || filepath.IsAbs(path) {
		return path, nil
	}
	// Path is relative from where the script was launched from.
	// When using `bazel run`, the environment variable BUILD_WORKING_DIRECTORY
	// will be set to the path where the command was run from.
	cwd := os.Getenv("BUILD_WORKING_DIRECTORY")
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return "", err
		}
	}
	return filepath.Abs(filepath.Join(cwd, path))
}

// Load loads a config from the given path.
func Load(path string) (*Config, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return nil, fmt.Errorf("failed to normalize path %q: %v", path, err)
	}
	m, err := loadMap(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load config to map: %v", err)
	}

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config map: %v", err)
	}

	conf := new(Config)
	if err := json.Unmarshal(b, conf); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}
	log.Printf("loaded config: %v", string(b))

	if err := conf.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize config: %v", err)
	}
	return conf, nil
}

type importsItem struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}

// loadMap loads the config at path into a map. It will also merge all imported configs.
// The given path should be absolute.
func loadMap(path string) (map[string]interface{}, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file at path %q: %v", path, err)
	}

	var raw json.RawMessage
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	root := make(map[string]interface{})
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("failed to unmarshal raw config to map: %v", err)
	}

	type config struct {
		Imports []*importsItem `json:"imports"`
	}
	conf := new(config)
	if err := json.Unmarshal(raw, conf); err != nil {
		return nil, fmt.Errorf("failed to unmarshal raw config to struct with imports: %v", err)
	}

	dir := filepath.Dir(path)
	pathMap := map[string]bool{
		path: true,
	}
	for _, imp := range conf.Imports {
		impPath := imp.Path
		if impPath == "" {
			continue
		}
		if !filepath.IsAbs(impPath) {
			impPath = filepath.Join(dir, impPath)
		}
		pathMap[impPath] = true

		impMap, err := loadMap(impPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load %q to map: %v", impPath, err)
		}
		if err := mergo.Merge(&root, impMap, mergo.WithAppendSlice); err != nil {
			return nil, fmt.Errorf("failed to merge imported file %q: %v", impPath, err)
		}
	}

	paths, err := patternPaths(path, conf.Imports)
	if err != nil {
		return nil, err
	}

	for _, p := range paths {
		if pathMap[p] {
			continue
		}
		impMap, err := loadMap(p)
		if err != nil {
			return nil, fmt.Errorf("failed to load %q to map: %v", p, err)
		}
		if err := mergo.Merge(&root, impMap, mergo.WithAppendSlice); err != nil {
			return nil, fmt.Errorf("failed to merge imported file %q: %v", p, err)
		}
	}
	return root, nil
}

// patternPaths returns all files matching the patterns defined
// in importsList.
// If projectYAMLPath match patterns, the result always ignore it.
// projectYAMLPath should be an absolute path.
// Patterns in importsList could be relative path to the projectYAMLPath
// or absolute paths.
// For example, if "./*.yaml" is an entry of "imports", the project YAML itself
// would match the pattern. We should exclude that path because we do not want to
// include the content of that YAML twice.
func patternPaths(projectYAMLPath string, importsList []*importsItem) ([]string, error) {
	allMatches := make(map[string]bool)
	projectYamlFolder := filepath.Dir(projectYAMLPath)
	for _, importItem := range importsList {
		// joinedPath would be always an absolute path (pattern).
		joinedPath := importItem.Pattern
		if joinedPath == "" {
			continue
		}
		if !filepath.IsAbs(joinedPath) {
			joinedPath = filepath.Join(projectYamlFolder, importItem.Pattern)
		}
		matches, err := filepath.Glob(joinedPath)
		if err != nil {
			return nil, fmt.Errorf("pattern %q is malformed", importItem.Pattern)
		}
		for _, match := range matches {
			if match == projectYAMLPath {
				continue
			}
			allMatches[match] = true
		}
	}
	var filePathList []string
	for path := range allMatches {
		filePathList = append(filePathList, path)
	}
	return filePathList, nil
}
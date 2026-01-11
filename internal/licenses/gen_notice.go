//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const thirdPartyHeader = `Melange Third-Party Notices

The following 3rd-party software packages may be used by or distributed with Melange. Certain licenses
and notices may appear in other parts of the product in accordance with the applicable license requirements.

Melange may not use all the open source software packages referred to below and may only use portions of a given
package.

Date generated: %s

`

const noticeSeparator = "================================================================================"
const fileSeparator = "--------------------------------------------------------------------------------"

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		exit(err)
	}

	thirdPartyDir := filepath.Join(cwd, "third_party")
	thirdPartyRootPath := filepath.Clean(filepath.Join(cwd, "..", "..", "THIRD_PARTY_NOTICES"))
	thirdPartyEmbedPath := filepath.Join(cwd, "assets", "THIRD_PARTY_NOTICES")

	moduleVersions, err := loadModuleVersions(filepath.Clean(filepath.Join(cwd, "..", "..")))
	if err != nil {
		exit(err)
	}

	moduleFiles, err := listModuleFiles(thirdPartyDir)
	if err != nil {
		exit(err)
	}

	var moduleNames []string
	for name := range moduleFiles {
		moduleNames = append(moduleNames, name)
	}
	sort.Strings(moduleNames)

	var toc []string
	for _, module := range moduleNames {
		files := moduleFiles[module]
		if len(files) == 0 {
			continue
		}
		moduleRoot, _ := resolveModuleVersion(module, moduleVersions)
		licenseName := detectLicenseName(files)
		toc = append(toc, fmt.Sprintf("- %s - %s", moduleRoot, licenseName))
	}

	var thirdPartyBuilder strings.Builder
	thirdPartyBuilder.WriteString(fmt.Sprintf(thirdPartyHeader, time.Now().Format("2006-01-02")))
	thirdPartyBuilder.WriteString("Dependencies:\n")
	thirdPartyBuilder.WriteString("\n")
	for _, entry := range toc {
		thirdPartyBuilder.WriteString(entry)
		thirdPartyBuilder.WriteString("\n")
	}
	thirdPartyBuilder.WriteString("\n")

	for _, module := range moduleNames {
		files := moduleFiles[module]
		if len(files) == 0 {
			continue
		}

		moduleRoot, version := resolveModuleVersion(module, moduleVersions)

		sort.Strings(files)
		copyrightLine := extractCopyrightLine(files)
		if copyrightLine == "" {
			copyrightLine = "unknown"
		}
		licenseName := detectLicenseName(files)

		thirdPartyBuilder.WriteString(noticeSeparator)
		thirdPartyBuilder.WriteString("\n")
		thirdPartyBuilder.WriteString("PACKAGE: ")
		thirdPartyBuilder.WriteString(moduleRoot)
		thirdPartyBuilder.WriteString("\n")
		thirdPartyBuilder.WriteString("LICENSE: ")
		thirdPartyBuilder.WriteString(licenseName)
		thirdPartyBuilder.WriteString("\n")
		thirdPartyBuilder.WriteString("VERSION: ")
		thirdPartyBuilder.WriteString(version)
		thirdPartyBuilder.WriteString("\n")
		if moduleRoot != module {
			thirdPartyBuilder.WriteString("PATH: ")
			thirdPartyBuilder.WriteString(module)
			thirdPartyBuilder.WriteString("\n")
		}
		thirdPartyBuilder.WriteString("COPYRIGHT: ")
		thirdPartyBuilder.WriteString(copyrightLine)
		thirdPartyBuilder.WriteString("\n")
		thirdPartyBuilder.WriteString("FILES: ")
		thirdPartyBuilder.WriteString(listFileNames(thirdPartyDir, files))
		thirdPartyBuilder.WriteString("\n")
		thirdPartyBuilder.WriteString(noticeSeparator)
		thirdPartyBuilder.WriteString("\n\n")

		for _, path := range files {
			rel, err := filepath.Rel(thirdPartyDir, path)
			if err != nil {
				exit(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				exit(err)
			}
			thirdPartyBuilder.WriteString(fileSeparator)
			thirdPartyBuilder.WriteString("\n")
			thirdPartyBuilder.WriteString("FILE: ")
			thirdPartyBuilder.WriteString(filepath.ToSlash(rel))
			thirdPartyBuilder.WriteString("\n")
			thirdPartyBuilder.WriteString(fileSeparator)
			thirdPartyBuilder.WriteString("\n\n")
			thirdPartyBuilder.WriteString(strings.TrimRight(string(data), "\n"))
			thirdPartyBuilder.WriteString("\n\n")
		}
	}

	if err := os.WriteFile(thirdPartyRootPath, []byte(thirdPartyBuilder.String()), 0o644); err != nil {
		exit(err)
	}
	if err := os.WriteFile(thirdPartyEmbedPath, []byte(thirdPartyBuilder.String()), 0o644); err != nil {
		exit(err)
	}
}

func listModuleFiles(root string) (map[string][]string, error) {
	modules := make(map[string][]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}

		var noticeFiles []string
		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if !isNoticeName(entry.Name()) {
				continue
			}
			noticeFiles = append(noticeFiles, filepath.Join(path, entry.Name()))
		}

		if len(noticeFiles) == 0 {
			return nil
		}

		sort.Strings(noticeFiles)
		modulePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		modulePath = filepath.ToSlash(modulePath)
		modules[modulePath] = append(modules[modulePath], noticeFiles...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return modules, nil
}

func isNoticeName(name string) bool {
	name = strings.ToUpper(name)
	switch {
	case strings.HasPrefix(name, "LICENSE"):
		return true
	case strings.HasPrefix(name, "NOTICE"):
		return true
	case strings.HasPrefix(name, "COPYING"):
		return true
	case strings.HasPrefix(name, "COPYRIGHT"):
		return true
	default:
		return false
	}
}

func listFileNames(root string, files []string) string {
	var names []string
	for _, path := range files {
		names = append(names, filepath.Base(path))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func extractCopyrightLine(files []string) string {
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		line := firstCopyrightLine(string(data))
		if line != "" {
			return line
		}
	}
	return ""
}

func firstCopyrightLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if (strings.HasPrefix(lower, "copyright") || strings.HasPrefix(trimmed, "©")) && hasDigit(trimmed) {
			return trimmed
		}
	}
	return ""
}

type moduleInfo struct {
	Path    string      `json:"Path"`
	Version string      `json:"Version"`
	Replace *moduleInfo `json:"Replace,omitempty"`
	Main    bool        `json:"Main,omitempty"`
}

func loadModuleVersions(root string) (map[string]string, error) {
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	versions := make(map[string]string)
	for {
		var mod moduleInfo
		if err := decoder.Decode(&mod); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if mod.Main {
			continue
		}
		if mod.Replace != nil && mod.Replace.Version != "" {
			versions[mod.Path] = mod.Replace.Version
			continue
		}
		versions[mod.Path] = mod.Version
	}
	return versions, nil
}

func detectLicenseName(files []string) string {
	for _, path := range preferLicenseFiles(files) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name := detectLicenseNameFromText(string(data))
		if name != "" {
			return name
		}
	}
	return "unknown"
}

func preferLicenseFiles(files []string) []string {
	type scored struct {
		path  string
		score int
	}
	var scoredFiles []scored
	for _, path := range files {
		name := strings.ToUpper(filepath.Base(path))
		score := 1
		switch {
		case strings.HasPrefix(name, "LICENSE"):
			score = 3
		case strings.HasPrefix(name, "COPYING"):
			score = 2
		}
		scoredFiles = append(scoredFiles, scored{path: path, score: score})
	}
	sort.SliceStable(scoredFiles, func(i, j int) bool {
		if scoredFiles[i].score == scoredFiles[j].score {
			return scoredFiles[i].path < scoredFiles[j].path
		}
		return scoredFiles[i].score > scoredFiles[j].score
	})
	ordered := make([]string, 0, len(scoredFiles))
	for _, entry := range scoredFiles {
		ordered = append(ordered, entry.path)
	}
	return ordered
}

func detectLicenseNameFromText(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "apache license") && strings.Contains(lower, "version 2.0"):
		return "Apache-2.0"
	case strings.Contains(lower, "mit license"):
		return "MIT"
	case strings.Contains(lower, "permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(lower, "bsd 3-clause") || strings.Contains(lower, "bsd-3-clause"):
		return "BSD-3-Clause"
	case strings.Contains(lower, "bsd 2-clause") || strings.Contains(lower, "bsd-2-clause"):
		return "BSD-2-Clause"
	case strings.Contains(lower, "isc license"):
		return "ISC"
	case strings.Contains(lower, "mozilla public license") && strings.Contains(lower, "version 2.0"):
		return "MPL-2.0"
	case strings.Contains(lower, "creative commons attribution-sharealike 4.0"):
		return "CC-BY-SA-4.0"
	case strings.Contains(lower, "creative commons attribution 4.0"):
		return "CC-BY-4.0"
	case strings.Contains(lower, "eclipse public license") && strings.Contains(lower, "2.0"):
		return "EPL-2.0"
	case strings.Contains(lower, "redistribution and use in source and binary forms") &&
		(strings.Contains(lower, "neither the name of") || strings.Contains(lower, "neither name of")):
		return "BSD-3-Clause"
	case strings.Contains(lower, "redistribution and use in source and binary forms") &&
		strings.Contains(lower, "this list of conditions") &&
		!strings.Contains(lower, "neither the name of"):
		return "BSD-2-Clause"
	case strings.Contains(lower, "permission to use, copy, modify, and/or distribute") &&
		strings.Contains(lower, "the software is provided \"as is\""):
		return "ISC"
	default:
		return ""
	}
}

func resolveModuleVersion(modulePath string, versions map[string]string) (string, string) {
	current := modulePath
	for {
		if version, ok := versions[current]; ok {
			if version == "" {
				version = "unknown"
			}
			return current, version
		}
		idx := strings.LastIndex(current, "/")
		if idx == -1 {
			break
		}
		current = current[:idx]
	}
	return modulePath, "unknown"
}

func hasDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

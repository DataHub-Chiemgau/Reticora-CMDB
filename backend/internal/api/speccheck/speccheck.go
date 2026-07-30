// Package speccheck reads the OpenAPI specification well enough to compare the
// documented operations with the routes the server actually registers.
//
// It intentionally implements a tiny, dependency-free reader for the subset of
// YAML the specification uses (two-space indentation, one operation per line)
// instead of pulling in a YAML library that would only ever be used by this
// check.
package speccheck

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Operation is one documented (method, path) pair. Methods are upper-case.
type Operation struct {
	Method string
	Path   string
}

// String renders the operation as it appears in diff output.
func (o Operation) String() string { return o.Method + " " + o.Path }

var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// Load parses the operations documented under the `paths:` key of an OpenAPI
// document.
func Load(path string) ([]Operation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spec: %w", err)
	}
	defer file.Close()

	var (
		operations []Operation
		inPaths    bool
		current    string
	)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		// A non-indented key ends the paths section.
		if !strings.HasPrefix(line, " ") {
			inPaths = strings.HasPrefix(line, "paths:")
			current = ""
			continue
		}
		if !inPaths {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)

		if indent == 2 && strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":") {
			current = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if indent == 4 && current != "" && strings.HasSuffix(trimmed, ":") {
			method := strings.ToLower(strings.TrimSuffix(trimmed, ":"))
			if httpMethods[method] {
				operations = append(operations, Operation{Method: strings.ToUpper(method), Path: current})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}

	sortOperations(operations)
	return operations, nil
}

// Diff reports the operations that exist only in one of the two sets.
func Diff(left, right []Operation) (onlyLeft, onlyRight []Operation) {
	index := func(ops []Operation) map[Operation]bool {
		m := make(map[Operation]bool, len(ops))
		for _, op := range ops {
			m[op] = true
		}
		return m
	}
	leftIndex, rightIndex := index(left), index(right)

	for _, op := range left {
		if !rightIndex[op] {
			onlyLeft = append(onlyLeft, op)
		}
	}
	for _, op := range right {
		if !leftIndex[op] {
			onlyRight = append(onlyRight, op)
		}
	}
	sortOperations(onlyLeft)
	sortOperations(onlyRight)
	return onlyLeft, onlyRight
}

func sortOperations(ops []Operation) {
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path == ops[j].Path {
			return ops[i].Method < ops[j].Method
		}
		return ops[i].Path < ops[j].Path
	})
}

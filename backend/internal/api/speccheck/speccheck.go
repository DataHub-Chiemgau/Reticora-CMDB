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

// Phases returns the x-phase extension of every path that carries one: the
// operations of late modules, which a release may switch off (WP-214).
func Phases(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spec: %w", err)
	}
	defer file.Close()

	phases := map[string]string{}
	inPaths, current := false, ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		if line == "" {
			continue
		}
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
		if indent == 4 && current != "" && strings.HasPrefix(trimmed, "x-phase:") {
			phases[current] = strings.TrimSpace(strings.TrimPrefix(trimmed, "x-phase:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	return phases, nil
}

// Features returns the feature key of every operation that carries one
// (x-feature, E-38, WP-706): the x-feature of the operation, else the one of
// its path. Operations without either are missing from the map.
func Features(path string) (map[Operation]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spec: %w", err)
	}
	defer file.Close()

	var (
		operations  []Operation
		pathFeature = map[string]string{}
		opFeature   = map[Operation]string{}
		inPaths     bool
		current     string
		method      string
	)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			inPaths = strings.HasPrefix(line, "paths:")
			current, method = "", ""
			continue
		}
		if !inPaths {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		switch {
		case indent == 2 && strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":"):
			current, method = strings.TrimSuffix(trimmed, ":"), ""
		case indent == 4 && current != "":
			method = ""
			if value, ok := strings.CutPrefix(trimmed, "x-feature:"); ok {
				pathFeature[current] = strings.TrimSpace(value)
				continue
			}
			if name := strings.ToLower(strings.TrimSuffix(trimmed, ":")); strings.HasSuffix(trimmed, ":") && httpMethods[name] {
				method = strings.ToUpper(name)
				operations = append(operations, Operation{Method: method, Path: current})
			}
		case indent == 6 && method != "":
			if value, ok := strings.CutPrefix(trimmed, "x-feature:"); ok {
				opFeature[Operation{Method: method, Path: current}] = strings.TrimSpace(value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}

	features := map[Operation]string{}
	for _, op := range operations {
		feature, ok := opFeature[op]
		if !ok {
			feature, ok = pathFeature[op.Path]
		}
		if ok {
			features[op] = feature
		}
	}
	return features, nil
}

// StatusPlanned is the only value of x-status: the operation is a scaffold
// of the path contract (E-38, WP-247) that no route serves yet.
const StatusPlanned = "planned"

// checkStatus accepts the value of an x-status line; any value other than
// planned is an error.
func checkStatus(value string) error {
	if value = strings.Trim(strings.TrimSpace(value), `'"`); value != StatusPlanned {
		return fmt.Errorf("x-status %q, the only value is %s", value, StatusPlanned)
	}
	return nil
}

// Planned returns the operations marked x-status: planned (E-38, WP-247):
// on the path level (indentation 4) the mark covers every operation of the
// path, on the operation level (indentation 6) that one operation. Any
// other value of x-status is an error. Load and Phases ignore the mark.
func Planned(path string) (map[Operation]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spec: %w", err)
	}
	defer file.Close()

	var (
		operations  []Operation
		pathPlanned = map[string]bool{}
		planned     = map[Operation]bool{}
		inPaths     bool
		current     string
		method      string
	)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			inPaths = strings.HasPrefix(line, "paths:")
			current, method = "", ""
			continue
		}
		if !inPaths {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		switch {
		case indent == 2 && strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":"):
			current, method = strings.TrimSuffix(trimmed, ":"), ""
		case indent == 4 && current != "":
			method = ""
			if value, ok := strings.CutPrefix(trimmed, "x-status:"); ok {
				if serr := checkStatus(value); serr != nil {
					return nil, fmt.Errorf("%s: %w", current, serr)
				}
				pathPlanned[current] = true
				continue
			}
			if name := strings.ToLower(strings.TrimSuffix(trimmed, ":")); strings.HasSuffix(trimmed, ":") && httpMethods[name] {
				method = strings.ToUpper(name)
				operations = append(operations, Operation{Method: method, Path: current})
			}
		case indent == 6 && method != "":
			if value, ok := strings.CutPrefix(trimmed, "x-status:"); ok {
				op := Operation{Method: method, Path: current}
				if serr := checkStatus(value); serr != nil {
					return nil, fmt.Errorf("%s: %w", op, serr)
				}
				planned[op] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	for _, op := range operations {
		if pathPlanned[op.Path] {
			planned[op] = true
		}
	}
	return planned, nil
}

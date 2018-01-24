// Copyright (c) 2013 The Go Authors. All rights reserved.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd.

package testutil

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"io/ioutil"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/mgechev/revive/rule"

	"github.com/mgechev/revive/linter"
)

var lintMatch = flag.String("lint.match", "", "restrict fixtures matches to this pattern")

var rules = []linter.Rule{&rule.VarDeclarationsRule{}}

func TestAll(t *testing.T) {
	baseDir := "../fixtures/"
	l := linter.New(func(file string) ([]byte, error) {
		return ioutil.ReadFile(baseDir + file)
	})
	rx, err := regexp.Compile(*lintMatch)
	if err != nil {
		t.Fatalf("Bad -lint.match value %q: %v", *lintMatch, err)
	}

	fis, err := ioutil.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("ioutil.ReadDir: %v", err)
	}
	if len(fis) == 0 {
		t.Fatalf("no files in %v", baseDir)
	}
	for _, fi := range fis {
		if !rx.MatchString(fi.Name()) {
			continue
		}
		//t.Logf("Testing %s", fi.Name())
		src, err := ioutil.ReadFile(path.Join(baseDir, fi.Name()))
		if err != nil {
			t.Fatalf("Failed reading %s: %v", fi.Name(), err)
		}

		ins := parseInstructions(t, fi.Name(), src)
		if ins == nil {
			t.Errorf("Test file %v does not have instructions", fi.Name())
			continue
		}

		ps, err := l.Lint([]string{fi.Name()}, rules, map[string]linter.Arguments{})
		if err != nil {
			t.Errorf("Linting %s: %v", fi.Name(), err)
			continue
		}

		failures := []linter.Failure{}
		for f := range ps {
			failures = append(failures, f)
		}

		for _, in := range ins {
			ok := false
			for i, p := range failures {
				if p.Position.Start.Line != in.Line {
					continue
				}
				if in.Match == p.Failure {
					// check replacement if we are expecting one
					if in.Replacement != "" {
						// ignore any inline comments, since that would be recursive
						r := p.ReplacementLine
						if i := strings.Index(r, " //"); i >= 0 {
							r = r[:i]
						}
						if r != in.Replacement {
							t.Errorf("Lint failed at %s:%d; got replacement %q, want %q", fi.Name(), in.Line, r, in.Replacement)
						}
					}

					// remove this problem from ps
					copy(failures[i:], failures[i+1:])
					failures = failures[:len(failures)-1]

					// t.Logf("/%v/ matched at %s:%d", in.Match, fi.Name(), in.Line)
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("Lint failed at %s:%d; /%v/ did not match", fi.Name(), in.Line, in.Match)
			}
		}
		for _, p := range failures {
			t.Errorf("Unexpected problem at %s:%d: %v", fi.Name(), p.Position.Start.Line, p.Failure)
		}
	}
}

type instruction struct {
	Line        int    // the line number this applies to
	Match       string // what pattern to match
	Replacement string // what the suggested replacement line should be
}

// parseInstructions parses instructions from the comments in a Go source file.
// It returns nil if none were parsed.
func parseInstructions(t *testing.T, filename string, src []byte) []instruction {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Test file %v does not parse: %v", filename, err)
	}
	var ins []instruction
	for _, cg := range f.Comments {
		ln := fset.Position(cg.Pos()).Line
		raw := cg.Text()
		for _, line := range strings.Split(raw, "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if line == "OK" && ins == nil {
				// so our return value will be non-nil
				ins = make([]instruction, 0)
				continue
			}
			if strings.Contains(line, "MATCH") {
				match, err := extractPattern(line)
				if err != nil {
					t.Fatalf("At %v:%d: %v", filename, ln, err)
				}
				matchLine := ln
				if i := strings.Index(line, "MATCH:"); i >= 0 {
					// This is a match for a different line.
					lns := strings.TrimPrefix(line[i:], "MATCH:")
					lns = lns[:strings.Index(lns, " ")]
					matchLine, err = strconv.Atoi(lns)
					if err != nil {
						t.Fatalf("Bad match line number %q at %v:%d: %v", lns, filename, ln, err)
					}
				}
				var repl string
				if r, ok := extractReplacement(line); ok {
					repl = r
				}
				ins = append(ins, instruction{
					Line:        matchLine,
					Match:       match,
					Replacement: repl,
				})
			}
		}
	}
	return ins
}

func extractPattern(line string) (string, error) {
	a, b := strings.Index(line, "/"), strings.LastIndex(line, "/")
	if a == -1 || a == b {
		return "", fmt.Errorf("malformed match instruction %q", line)
	}
	return line[a+1 : b], nil
}

func extractReplacement(line string) (string, bool) {
	// Look for this:  / -> `
	// (the end of a match and start of a backtick string),
	// and then the closing backtick.
	const start = "/ -> `"
	a, b := strings.Index(line, start), strings.LastIndex(line, "`")
	if a < 0 || a > b {
		return "", false
	}
	return line[a+len(start) : b], true
}

func render(fset *token.FileSet, x interface{}) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, x); err != nil {
		panic(err)
	}
	return buf.String()
}

func srcLine(src []byte, p token.Position) string {
	// Run to end of line in both directions if not at line start/end.
	lo, hi := p.Offset, p.Offset+1
	for lo > 0 && src[lo-1] != '\n' {
		lo--
	}
	for hi < len(src) && src[hi-1] != '\n' {
		hi++
	}
	return string(src[lo:hi])
}

func TestLine(t *testing.T) {
	tests := []struct {
		src    string
		offset int
		want   string
	}{
		{"single line file", 5, "single line file"},
		{"single line file with newline\n", 5, "single line file with newline\n"},
		{"first\nsecond\nthird\n", 2, "first\n"},
		{"first\nsecond\nthird\n", 9, "second\n"},
		{"first\nsecond\nthird\n", 14, "third\n"},
		{"first\nsecond\nthird with no newline", 16, "third with no newline"},
		{"first byte\n", 0, "first byte\n"},
	}
	for _, test := range tests {
		got := srcLine([]byte(test.src), token.Position{Offset: test.offset})
		if got != test.want {
			t.Errorf("srcLine(%q, offset=%d) = %q, want %q", test.src, test.offset, got, test.want)
		}
	}
}

// commonInitialisms is a set of common initialisms.
// Only add entries that are highly unlikely to be non-initialisms.
// For instance, "ID" is fine (Freudian code is rare), but "AND" is not.
var commonInitialisms = map[string]bool{
	"ACL":   true,
	"API":   true,
	"ASCII": true,
	"CPU":   true,
	"CSS":   true,
	"DNS":   true,
	"EOF":   true,
	"GUID":  true,
	"HTML":  true,
	"HTTP":  true,
	"HTTPS": true,
	"ID":    true,
	"IP":    true,
	"JSON":  true,
	"LHS":   true,
	"QPS":   true,
	"RAM":   true,
	"RHS":   true,
	"RPC":   true,
	"SLA":   true,
	"SMTP":  true,
	"SQL":   true,
	"SSH":   true,
	"TCP":   true,
	"TLS":   true,
	"TTL":   true,
	"UDP":   true,
	"UI":    true,
	"UID":   true,
	"UUID":  true,
	"URI":   true,
	"URL":   true,
	"UTF8":  true,
	"VM":    true,
	"XML":   true,
	"XMPP":  true,
	"XSRF":  true,
	"XSS":   true,
}

// lintName returns a different name if it should be different.
func lintName(name string) (should string) {
	// Fast path for simple cases: "_" and all lowercase.
	if name == "_" {
		return name
	}
	allLower := true
	for _, r := range name {
		if !unicode.IsLower(r) {
			allLower = false
			break
		}
	}
	if allLower {
		return name
	}

	// Split camelCase at any lower->upper transition, and split on underscores.
	// Check each word for common initialisms.
	runes := []rune(name)
	w, i := 0, 0 // index of start of word, scan
	for i+1 <= len(runes) {
		eow := false // whether we hit the end of a word
		if i+1 == len(runes) {
			eow = true
		} else if runes[i+1] == '_' {
			// underscore; shift the remainder forward over any run of underscores
			eow = true
			n := 1
			for i+n+1 < len(runes) && runes[i+n+1] == '_' {
				n++
			}

			// Leave at most one underscore if the underscore is between two digits
			if i+n+1 < len(runes) && unicode.IsDigit(runes[i]) && unicode.IsDigit(runes[i+n+1]) {
				n--
			}

			copy(runes[i+1:], runes[i+n+1:])
			runes = runes[:len(runes)-n]
		} else if unicode.IsLower(runes[i]) && !unicode.IsLower(runes[i+1]) {
			// lower->non-lower
			eow = true
		}
		i++
		if !eow {
			continue
		}

		// [w,i) is a word.
		word := string(runes[w:i])
		if u := strings.ToUpper(word); commonInitialisms[u] {
			// Keep consistent case, which is lowercase only at the start.
			if w == 0 && unicode.IsLower(runes[w]) {
				u = strings.ToLower(u)
			}
			// All the common initialisms are ASCII,
			// so we can replace the bytes exactly.
			copy(runes[w:], []rune(u))
		} else if w > 0 && strings.ToLower(word) == word {
			// already all lowercase, and not the first word, so uppercase the first character.
			runes[w] = unicode.ToUpper(runes[w])
		}
		w = i
	}
	return string(runes)
}

func TestLintName(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		{"foo_bar", "fooBar"},
		{"foo_bar_baz", "fooBarBaz"},
		{"Foo_bar", "FooBar"},
		{"foo_WiFi", "fooWiFi"},
		{"id", "id"},
		{"Id", "ID"},
		{"foo_id", "fooID"},
		{"fooId", "fooID"},
		{"fooUid", "fooUID"},
		{"idFoo", "idFoo"},
		{"uidFoo", "uidFoo"},
		{"midIdDle", "midIDDle"},
		{"APIProxy", "APIProxy"},
		{"ApiProxy", "APIProxy"},
		{"apiProxy", "apiProxy"},
		{"_Leading", "_Leading"},
		{"___Leading", "_Leading"},
		{"trailing_", "trailing"},
		{"trailing___", "trailing"},
		{"a_b", "aB"},
		{"a__b", "aB"},
		{"a___b", "aB"},
		{"Rpc1150", "RPC1150"},
		{"case3_1", "case3_1"},
		{"case3__1", "case3_1"},
		{"IEEE802_16bit", "IEEE802_16bit"},
		{"IEEE802_16Bit", "IEEE802_16Bit"},
	}
	for _, test := range tests {
		got := lintName(test.name)
		if got != test.want {
			t.Errorf("lintName(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

// exportedType reports whether typ is an exported type.
// It is imprecise, and will err on the side of returning true,
// such as for composite types.
func exportedType(typ types.Type) bool {
	switch T := typ.(type) {
	case *types.Named:
		// Builtin types have no package.
		return T.Obj().Pkg() == nil || T.Obj().Exported()
	case *types.Map:
		return exportedType(T.Key()) && exportedType(T.Elem())
	case interface {
		Elem() types.Type
	}: // array, slice, pointer, chan
		return exportedType(T.Elem())
	}
	// Be conservative about other types, such as struct, interface, etc.
	return true
}

func TestExportedType(t *testing.T) {
	tests := []struct {
		typString string
		exp       bool
	}{
		{"int", true},
		{"string", false}, // references the shadowed builtin "string"
		{"T", true},
		{"t", false},
		{"*T", true},
		{"*t", false},
		{"map[int]complex128", true},
	}
	for _, test := range tests {
		src := `package foo; type T int; type t int; type string struct{}`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "foo.go", src, 0)
		if err != nil {
			t.Fatalf("Parsing %q: %v", src, err)
		}
		// use the package name as package path
		config := &types.Config{}
		pkg, err := config.Check(file.Name.Name, fset, []*ast.File{file}, nil)
		if err != nil {
			t.Fatalf("Type checking %q: %v", src, err)
		}
		tv, err := types.Eval(fset, pkg, token.NoPos, test.typString)
		if err != nil {
			t.Errorf("types.Eval(%q): %v", test.typString, err)
			continue
		}
		if got := exportedType(tv.Type); got != test.exp {
			t.Errorf("exportedType(%v) = %t, want %t", tv.Type, got, test.exp)
		}
	}
}

var (
	genHdr = []byte("// Code generated ")
	genFtr = []byte(" DO NOT EDIT.")
)

// isGenerated reports whether the source file is generated code
// according the rules from https://golang.org/s/generatedcode.
func isGenerated(src []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		b := sc.Bytes()
		if bytes.HasPrefix(b, genHdr) && bytes.HasSuffix(b, genFtr) && len(b) >= len(genHdr)+len(genFtr) {
			return true
		}
	}
	return false
}

func TestIsGenerated(t *testing.T) {
	tests := []struct {
		source    string
		generated bool
	}{
		{"// Code Generated by some tool. DO NOT EDIT.", false},
		{"// Code generated by some tool. DO NOT EDIT.", true},
		{"// Code generated by some tool. DO NOT EDIT", false},
		{"// Code generated  DO NOT EDIT.", true},
		{"// Code generated DO NOT EDIT.", false},
		{"\t\t// Code generated by some tool. DO NOT EDIT.\npackage foo\n", false},
		{"// Code generated by some tool. DO NOT EDIT.\npackage foo\n", true},
		{"package foo\n// Code generated by some tool. DO NOT EDIT.\ntype foo int\n", true},
		{"package foo\n // Code generated by some tool. DO NOT EDIT.\ntype foo int\n", false},
		{"package foo\n// Code generated by some tool. DO NOT EDIT. \ntype foo int\n", false},
		{"package foo\ntype foo int\n// Code generated by some tool. DO NOT EDIT.\n", true},
		{"package foo\ntype foo int\n// Code generated by some tool. DO NOT EDIT.", true},
	}

	for i, test := range tests {
		got := isGenerated([]byte(test.source))
		if got != test.generated {
			t.Errorf("test %d, isGenerated() = %v, want %v", i, got, test.generated)
		}
	}
}
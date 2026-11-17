package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A small flag parser that, unlike the standard library's, lets flags and
// positional arguments mix freely: `exec "fix it" --model x` works.

type flagKind int

const (
	fBool flagKind = iota
	fString
	fInt
	fList
	fOptional // --name or --name=value; never consumes the next argument
)

type flagSpec struct {
	names  []string
	kind   flagKind
	help   string
	value  string // metavar
	target any
}

type flagSet struct {
	cmd   string
	specs []*flagSpec
	byKey map[string]*flagSpec
	args  []string // positional arguments after Parse
}

func newFlags(cmd string) *flagSet {
	return &flagSet{cmd: cmd, byKey: map[string]*flagSpec{}}
}

func (f *flagSet) add(s *flagSpec) {
	f.specs = append(f.specs, s)
	for _, n := range s.names {
		f.byKey[n] = s
	}
}

func (f *flagSet) Bool(p *bool, help string, names ...string) {
	f.add(&flagSpec{names: names, kind: fBool, help: help, target: p})
}

func (f *flagSet) String(p *string, metavar, help string, names ...string) {
	f.add(&flagSpec{names: names, kind: fString, help: help, value: metavar, target: p})
}

func (f *flagSet) Int(p *int, metavar, help string, names ...string) {
	f.add(&flagSpec{names: names, kind: fInt, help: help, value: metavar, target: p})
}

func (f *flagSet) List(p *[]string, metavar, help string, names ...string) {
	f.add(&flagSpec{names: names, kind: fList, help: help, value: metavar, target: p})
}

// Optional registers --name (sets *set) or --name=value (also sets *val).
func (f *flagSet) Optional(set *bool, val *string, metavar, help string, names ...string) {
	f.add(&flagSpec{names: names, kind: fOptional, help: help, value: metavar, target: [2]any{set, val}})
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func (f *flagSet) Parse(args []string) error {
	f.args = nil
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			f.args = append(f.args, args[i+1:]...)
			return nil
		}
		if len(a) < 2 || a[0] != '-' || a == "-" {
			f.args = append(f.args, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		spec, ok := f.byKey[name]
		if !ok {
			return usageError{fmt.Sprintf("unknown flag %s for %s (see kalonide %s --help)", a, f.cmd, f.cmd)}
		}
		need := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", usageError{fmt.Sprintf("%s needs a value", a)}
			}
			i++
			return args[i], nil
		}
		switch spec.kind {
		case fBool:
			b := true
			if hasVal {
				parsed, err := strconv.ParseBool(val)
				if err != nil {
					return usageError{fmt.Sprintf("%s takes true or false", a)}
				}
				b = parsed
			}
			*spec.target.(*bool) = b
		case fString:
			v, err := need()
			if err != nil {
				return err
			}
			*spec.target.(*string) = v
		case fInt:
			v, err := need()
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return usageError{fmt.Sprintf("%s takes a number, got %q", a, v)}
			}
			*spec.target.(*int) = n
		case fList:
			v, err := need()
			if err != nil {
				return err
			}
			p := spec.target.(*[]string)
			*p = append(*p, v)
		case fOptional:
			t := spec.target.([2]any)
			*t[0].(*bool) = true
			if hasVal {
				*t[1].(*string) = val
			}
		}
	}
	return nil
}

// Usage renders the flag list.
func (f *flagSet) Usage() string {
	var rows [][2]string
	width := 0
	for _, s := range f.specs {
		var names []string
		for _, n := range s.names {
			if len(n) == 1 {
				names = append(names, "-"+n)
			} else {
				names = append(names, "--"+n)
			}
		}
		sort.Slice(names, func(i, j int) bool { return len(names[i]) < len(names[j]) })
		left := strings.Join(names, ", ")
		switch s.kind {
		case fString, fInt, fList:
			left += " " + s.value
		case fOptional:
			left += "[=" + s.value + "]"
		}
		rows = append(rows, [2]string{left, s.help})
		width = max(width, len(left))
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, r[0], r[1])
	}
	return b.String()
}

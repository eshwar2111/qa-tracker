// Package cli is Claude's interface to qa-tracker. Every write is recorded
// with actor "claude"; output is JSON unless --table is given.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"qa-tracker/internal/server"
	"qa-tracker/internal/store"
)

const usage = `qa — test cases & bug-fix loop

Usage: qa <command> [args] [--db path] [--project key] [--table]

  serve [--addr 127.0.0.1:7777]          start the web UI + API
  project add <key> [--name] [--repo]    create a project
  project list
  import <file.json> [--archive-missing] upsert areas + cases by key
  export [--out file.json]               dump cases in import format
  list [--status s1,s2] [--area] [--priority] [--q text]
  bugs [--status fail,blocked,in_fix] [--area]   open bugs with remarks
  case show <id|key>                     case + full timeline
  claim <id|key>...                      mark bug(s) as being fixed
  fixed <id|key>... --note "…" [--commit sha] [--files a.go,b.go]
  comment <id|key> "text"
  run new [--build sha] [--name] [--filter fixed|needs-retest|open|all|area:x|priority:P0]
                                         (default: fixed if any, else needs-retest, else all)
  run list | run show <id> | run close <id>
  summary                                dashboard numbers

DB: --db › $QA_DB › qa.db next to the executable.
`

// Run executes one CLI invocation and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	c := &ctx{out: stdout, errw: stderr}
	if err := c.dispatch(args[0], args[1:]); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	return 0
}

type ctx struct {
	out, errw io.Writer
	st        *store.Store
	dbPath    string
	project   string
	table     bool
}

// flags returns a FlagSet carrying the global flags.
func (c *ctx) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.errw)
	fs.StringVar(&c.dbPath, "db", "", "database path")
	fs.StringVar(&c.project, "project", "", "project key")
	fs.BoolVar(&c.table, "table", false, "human-readable output")
	return fs
}

// parse handles flags appearing before, between, or after positionals.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func dbPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("QA_DB"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "qa.db")
	}
	return "qa.db"
}

func (c *ctx) open() error {
	st, err := store.Open(dbPath(c.dbPath))
	if err != nil {
		return err
	}
	c.st = st
	return nil
}

func (c *ctx) proj() (*store.Project, error) { return c.st.ResolveProject(c.project) }

func (c *ctx) json(v any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (c *ctx) dispatch(cmd string, args []string) error {
	switch cmd {
	case "serve":
		return c.serve(args)
	case "project":
		return c.projectCmd(args)
	case "import":
		return c.importCmd(args)
	case "export":
		return c.exportCmd(args)
	case "list":
		return c.listCmd(args)
	case "bugs":
		return c.bugsCmd(args)
	case "case":
		return c.caseCmd(args)
	case "claim":
		return c.claimCmd(args)
	case "fixed":
		return c.fixedCmd(args)
	case "comment":
		return c.commentCmd(args)
	case "run":
		return c.runCmd(args)
	case "summary":
		return c.summaryCmd(args)
	}
	return fmt.Errorf("unknown command %q (try: qa help)", cmd)
}

// setup parses flags, opens the DB, and returns positionals.
func (c *ctx) setup(fs *flag.FlagSet, args []string) ([]string, error) {
	pos, err := parse(fs, args)
	if err != nil {
		return nil, err
	}
	return pos, c.open()
}

func (c *ctx) serve(args []string) error {
	fs := c.flags("serve")
	addr := fs.String("addr", "127.0.0.1:7777", "listen address")
	if _, err := c.setup(fs, args); err != nil {
		return err
	}
	defer c.st.Close()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "qa-tracker on http://%s  (db: %s)\n", ln.Addr(), dbPath(c.dbPath))
	return http.Serve(ln, server.New(c.st))
}

func (c *ctx) projectCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: qa project add <key> | qa project list")
	}
	fs := c.flags("project")
	name := fs.String("name", "", "display name")
	repo := fs.String("repo", "", "repository path")
	pos, err := c.setup(fs, args[1:])
	if err != nil {
		return err
	}
	defer c.st.Close()
	switch args[0] {
	case "add":
		if len(pos) != 1 {
			return errors.New("usage: qa project add <key> [--name] [--repo]")
		}
		p, err := c.st.CreateProject(pos[0], *name, *repo)
		if err != nil {
			return err
		}
		return c.json(p)
	case "list":
		ps, err := c.st.ListProjects()
		if err != nil {
			return err
		}
		return c.json(ps)
	}
	return fmt.Errorf("unknown project subcommand %q", args[0])
}

func (c *ctx) importCmd(args []string) error {
	fs := c.flags("import")
	archive := fs.Bool("archive-missing", false, "archive cases absent from the file")
	pos, err := c.setup(fs, args)
	if err != nil {
		return err
	}
	defer c.st.Close()
	if len(pos) != 1 {
		return errors.New("usage: qa import <file.json> [--archive-missing]")
	}
	b, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	var f store.ImportFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("parse %s: %w", pos[0], err)
	}
	if f.Project == "" {
		p, err := c.proj()
		if err != nil {
			return err
		}
		f.Project = p.Key
	}
	res, err := c.st.Import(f, *archive, store.ActorClaude)
	if err != nil {
		return err
	}
	return c.json(res)
}

func (c *ctx) exportCmd(args []string) error {
	fs := c.flags("export")
	out := fs.String("out", "", "output file (default stdout)")
	if _, err := c.setup(fs, args); err != nil {
		return err
	}
	defer c.st.Close()
	p, err := c.proj()
	if err != nil {
		return err
	}
	f, err := c.st.Export(p.ID)
	if err != nil {
		return err
	}
	if *out == "" {
		return c.json(f)
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	return os.WriteFile(*out, append(b, '\n'), 0o644)
}

func splitCSV(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (c *ctx) listCmd(args []string) error {
	fs := c.flags("list")
	status := fs.String("status", "", "comma-separated statuses")
	area := fs.String("area", "", "area key")
	prio := fs.String("priority", "", "P0..P3")
	q := fs.String("q", "", "search text")
	if _, err := c.setup(fs, args); err != nil {
		return err
	}
	defer c.st.Close()
	p, err := c.proj()
	if err != nil {
		return err
	}
	cs, err := c.st.ListCases(p.ID, store.CaseFilter{Statuses: splitCSV(*status), AreaKey: *area, Priority: *prio, Q: *q})
	if err != nil {
		return err
	}
	if c.table {
		tw := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKEY\tPRI\tSTATUS\tTITLE")
		for _, x := range cs {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", x.ID, x.Key, x.Priority, x.Status, x.Title)
		}
		return tw.Flush()
	}
	return c.json(cs)
}

func (c *ctx) bugsCmd(args []string) error {
	fs := c.flags("bugs")
	status := fs.String("status", "", "comma-separated statuses (default fail,blocked,in_fix)")
	area := fs.String("area", "", "area key")
	if _, err := c.setup(fs, args); err != nil {
		return err
	}
	defer c.st.Close()
	p, err := c.proj()
	if err != nil {
		return err
	}
	bugs, err := c.st.Bugs(p.ID, splitCSV(*status), *area)
	if err != nil {
		return err
	}
	if c.table {
		for _, b := range bugs {
			fmt.Fprintf(c.out, "#%d %s [%s %s %s] reopened×%d\n  %s\n  remarks: %s\n\n", b.ID, b.Key, b.Priority, b.Status, b.Severity, b.ReopenCount, b.Title, b.Remarks)
		}
		fmt.Fprintf(c.out, "%d open bug(s)\n", len(bugs))
		return nil
	}
	return c.json(map[string]any{"project": p.Key, "repo_path": p.RepoPath, "count": len(bugs), "bugs": bugs})
}

func (c *ctx) caseCmd(args []string) error {
	if len(args) == 0 || args[0] != "show" {
		return errors.New("usage: qa case show <id|key>")
	}
	fs := c.flags("case")
	pos, err := c.setup(fs, args[1:])
	if err != nil {
		return err
	}
	defer c.st.Close()
	if len(pos) != 1 {
		return errors.New("usage: qa case show <id|key>")
	}
	p, err := c.proj()
	if err != nil {
		return err
	}
	cs, err := c.st.ResolveCase(p.ID, pos[0])
	if err != nil {
		return err
	}
	d, err := c.st.CaseDetail(cs.ID)
	if err != nil {
		return err
	}
	return c.json(d)
}

// eachCase resolves refs and applies fn, continuing past failures so one bad
// ref doesn't block the rest; it returns an error if any failed.
func (c *ctx) eachCase(refs []string, fn func(id int64) (*store.Case, error)) error {
	p, err := c.proj()
	if err != nil {
		return err
	}
	type row struct {
		Ref    string `json:"ref"`
		ID     int64  `json:"id,omitempty"`
		Key    string `json:"key,omitempty"`
		Status string `json:"status,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	var rows []row
	failed := 0
	for _, ref := range refs {
		cs, err := c.st.ResolveCase(p.ID, ref)
		if err == nil {
			cs, err = fn(cs.ID)
		}
		if err != nil {
			failed++
			rows = append(rows, row{Ref: ref, Error: err.Error()})
			continue
		}
		rows = append(rows, row{Ref: ref, ID: cs.ID, Key: cs.Key, Status: cs.Status})
	}
	if err := c.json(rows); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, len(refs))
	}
	return nil
}

func (c *ctx) claimCmd(args []string) error {
	fs := c.flags("claim")
	pos, err := c.setup(fs, args)
	if err != nil {
		return err
	}
	defer c.st.Close()
	if len(pos) == 0 {
		return errors.New("usage: qa claim <id|key>...")
	}
	return c.eachCase(pos, func(id int64) (*store.Case, error) { return c.st.Claim(id, store.ActorClaude) })
}

func (c *ctx) fixedCmd(args []string) error {
	fs := c.flags("fixed")
	note := fs.String("note", "", "what changed and what to retest (required)")
	commit := fs.String("commit", "", "commit hash")
	files := fs.String("files", "", "comma-separated changed files")
	pos, err := c.setup(fs, args)
	if err != nil {
		return err
	}
	defer c.st.Close()
	if len(pos) == 0 {
		return errors.New(`usage: qa fixed <id|key>... --note "…" [--commit sha] [--files a,b]`)
	}
	in := store.FixInput{Note: *note, Commit: *commit, Files: splitCSV(*files)}
	return c.eachCase(pos, func(id int64) (*store.Case, error) { return c.st.MarkFixed(id, in, store.ActorClaude) })
}

func (c *ctx) commentCmd(args []string) error {
	fs := c.flags("comment")
	pos, err := c.setup(fs, args)
	if err != nil {
		return err
	}
	defer c.st.Close()
	if len(pos) < 2 {
		return errors.New(`usage: qa comment <id|key> "text"`)
	}
	return c.eachCase(pos[:1], func(id int64) (*store.Case, error) {
		if err := c.st.Comment(id, strings.Join(pos[1:], " "), store.ActorClaude); err != nil {
			return nil, err
		}
		return c.st.GetCase(id)
	})
}

func (c *ctx) runCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: qa run new|list|show|close")
	}
	fs := c.flags("run")
	build := fs.String("build", "", "build/commit under test")
	name := fs.String("name", "", "run name")
	filter := fs.String("filter", "", "fixed|needs-retest|open|all|area:<key>|priority:<P>")
	pos, err := c.setup(fs, args[1:])
	if err != nil {
		return err
	}
	defer c.st.Close()
	runID := func() (int64, error) {
		if len(pos) != 1 {
			return 0, fmt.Errorf("usage: qa run %s <id>", args[0])
		}
		return strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
	}
	switch args[0] {
	case "new":
		p, err := c.proj()
		if err != nil {
			return err
		}
		r, err := c.st.CreateRun(p.ID, *name, *build, *filter, store.ActorClaude)
		if err != nil {
			return err
		}
		return c.json(r)
	case "list":
		p, err := c.proj()
		if err != nil {
			return err
		}
		rs, err := c.st.ListRuns(p.ID)
		if err != nil {
			return err
		}
		return c.json(rs)
	case "show":
		id, err := runID()
		if err != nil {
			return err
		}
		d, err := c.st.GetRun(id)
		if err != nil {
			return err
		}
		return c.json(d)
	case "close":
		id, err := runID()
		if err != nil {
			return err
		}
		if err := c.st.CloseRun(id, store.ActorClaude); err != nil {
			return err
		}
		return c.json(map[string]any{"closed": id})
	}
	return fmt.Errorf("unknown run subcommand %q", args[0])
}

func (c *ctx) summaryCmd(args []string) error {
	fs := c.flags("summary")
	if _, err := c.setup(fs, args); err != nil {
		return err
	}
	defer c.st.Close()
	p, err := c.proj()
	if err != nil {
		return err
	}
	s, err := c.st.Summary(p.ID)
	if err != nil {
		return err
	}
	s.Recent = nil
	return c.json(s)
}

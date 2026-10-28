package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/blogwatch"
	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/spf13/cobra"
)

// exitBlogWatchScreened is `blog-watch screen`'s exit code when the post was
// blocked by the sanitization screen and recorded Declined.
const exitBlogWatchScreened = 3

// blogWatchLogName is the decision log inside CHECKOUT.
const blogWatchLogName = layout.CheckoutBlogWatch + "/decisions.jsonl"

// Seams swapped in tests.
var (
	blogWatchHTTPClient = func() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
	blogWatchConfig     = func(workdir string) config.BlogWatchConfig {
		cfg, _ := config.Load(workdir)
		return config.ResolveBlogWatch(cfg)
	}
	newBlogWatchFiler = func(cmd *cobra.Command, owner, repo string, project int) (blogWatchFiler, error) {
		client, err := clientFromConfig()
		if err != nil {
			return nil, err
		}
		ownerPart, repoPart := splitRepo(owner, repo)
		return &githubBlogWatchFiler{
			client: client, owner: ownerPart, repo: repoPart,
			projSvc: gh.NewProjectService(client, ownerPart, project, getOwnerType(cmd)),
		}, nil
	}
)

// blogWatchFiler creates one Adopted issue and returns its number.
type blogWatchFiler interface {
	Create(ctx context.Context, title, body string, labels []string) (int, error)
}

type githubBlogWatchFiler struct {
	client      *gh.Client
	owner, repo string
	projSvc     *gh.ProjectService
}

// Create files the issue, adds it to the board and parks it in Backlog. The
// watcher never moves a row past Backlog.
func (g *githubBlogWatchFiler) Create(ctx context.Context, title, body string, labels []string) (int, error) {
	_, labelIDs, err := resolveLabelNames(ctx, g.client, g.owner, g.repo, labels)
	if err != nil {
		return 0, err
	}
	repoID, err := g.client.GetRepositoryID(ctx, g.owner, g.repo)
	if err != nil {
		return 0, err
	}
	issue, err := gh.NewIssueService(g.client).CreateIssue(ctx, repoID, title, body, labelIDs)
	if err != nil {
		return 0, err
	}
	itemID, err := g.projSvc.AddItem(ctx, issue.NodeID)
	if err != nil {
		return issue.Number, fmt.Errorf("add #%d to project: %w", issue.Number, err)
	}
	if err := g.projSvc.SetSingleSelectField(ctx, itemID, "Status", "Backlog"); err != nil {
		return issue.Number, fmt.Errorf("set Status on #%d: %w", issue.Number, err)
	}
	return issue.Number, nil
}

// blogWatchCmd is the "blog-watch" command family behind
// skills/nightgauge-blog-watch (#185).
func blogWatchCmd() *cobra.Command {
	var logPath, workdir string
	cmd := &cobra.Command{
		Use:   "blog-watch",
		Short: "Detect, screen and record a verdict for every allowlisted blog post",
		Long: `Deterministic verbs behind the nightgauge-blog-watch skill. Sources and the
https host allowlist come from blog_watch in config; a URL off the allowlist
is never fetched. Every post gets exactly one row in the append-only decision
log (` + layout.CheckoutDisplay(layout.CheckoutBlogWatch, "decisions.jsonl") + `), with one of
the verdicts: Adopted, Already covered, Already filed, Declined, Deferred.`,
	}
	cmd.PersistentFlags().StringVar(&logPath, "log", "", "Decision log path (default: the checkout's "+blogWatchLogName+")")
	cmd.PersistentFlags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	env := &blogWatchEnv{log: &logPath, workdir: &workdir}
	cmd.AddCommand(blogWatchDetectCmd(env), blogWatchScreenCmd(env), blogWatchRecordCmd(env), blogWatchAdoptCmd(env))
	return cmd
}

type blogWatchEnv struct{ log, workdir *string }

func (e *blogWatchEnv) dir() (string, error) {
	if *e.workdir != "" {
		return *e.workdir, nil
	}
	return os.Getwd()
}

func (e *blogWatchEnv) logFile() (string, error) {
	if *e.log != "" {
		return *e.log, nil
	}
	dir, err := e.dir()
	if err != nil {
		return "", err
	}
	if _, err := layout.CheckoutSubdir(dir, layout.CheckoutBlogWatch); err != nil {
		return "", err
	}
	return layout.CheckoutPath(dir, blogWatchLogName)
}

func (e *blogWatchEnv) fetcher() (*blogwatch.Fetcher, []blogwatch.Source, error) {
	dir, err := e.dir()
	if err != nil {
		return nil, nil, err
	}
	bw := blogWatchConfig(dir)
	sources := make([]blogwatch.Source, len(bw.Sources))
	for i, s := range bw.Sources {
		sources[i] = blogwatch.Source{Name: s.Name, IndexURL: s.IndexURL, PostURLPrefix: s.PostURLPrefix}
	}
	return &blogwatch.Fetcher{Client: blogWatchHTTPClient(), AllowedHosts: bw.AllowedHosts}, sources, nil
}

func writeBlogWatchJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func blogWatchNow() string { return time.Now().UTC().Format(time.RFC3339) }

func blogWatchDetectCmd(env *blogWatchEnv) *cobra.Command {
	var jsonOutput, baseline bool
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "List posts on the configured sources that have no verdict yet",
		Long: `Fetches each allowlisted source index and prints the post URLs not yet in the
decision log: {new[], skipped[{source, reason}], known_count}. A source that is
off the allowlist, fails, or yields no post URL is listed under skipped.

--baseline records every new URL as Deferred ("baseline: pre-existing post"),
once, so a first run does not ask for a verdict on the whole archive.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := env.logFile()
			if err != nil {
				return err
			}
			log, err := blogwatch.LoadLog(path)
			if err != nil {
				return err
			}
			f, sources, err := env.fetcher()
			if err != nil {
				return err
			}
			res := blogwatch.Detect(cmd.Context(), f, sources, log)
			if baseline {
				for _, p := range res.New {
					if err := blogwatch.Append(path, blogwatch.Decision{
						URL: p.URL, Source: p.Source, Verdict: blogwatch.VerdictDeferred,
						Reason: "baseline: pre-existing post; reopen on operator re-evaluation", RecordedAt: blogWatchNow(),
					}); err != nil {
						return err
					}
				}
			}
			if jsonOutput {
				return writeBlogWatchJSON(cmd.OutOrStdout(), res)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%d new post(s), %d already recorded\n", len(res.New), res.KnownCount)
			for _, p := range res.New {
				fmt.Fprintf(w, "  new  %s (%s)\n", p.URL, p.Source)
			}
			for _, s := range res.Skipped {
				fmt.Fprintf(w, "  skip %s: %s\n", s.Source, s.Reason)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON (parsed by the skill)")
	cmd.Flags().BoolVar(&baseline, "baseline", false, "Record every new URL as Deferred (first run only)")
	return cmd
}

func blogWatchScreenCmd(env *blogWatchEnv) *cobra.Command {
	var postURL, out, source string
	cmd := &cobra.Command{
		Use:   "screen",
		Short: "Fetch a post and screen its text in block mode",
		Long: `Fetches --url (https and allowlisted only), strips it to text and screens it
for prompt injection in block mode, whatever sanitization.mode says. On allow
the text is written to --out. On block the post is recorded Declined with
reason "sanitization: <rule>", nothing is written, and the exit code is 3.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if postURL == "" || out == "" {
				return fmt.Errorf("--url and --out are required")
			}
			path, err := env.logFile()
			if err != nil {
				return err
			}
			f, _, err := env.fetcher()
			if err != nil {
				return err
			}
			doc, err := f.Fetch(cmd.Context(), postURL)
			if err != nil {
				return err
			}
			text := blogwatch.HTMLToText(doc)
			if d := hooks.EvaluateSanitizeText(text, config.SanitizationModeBlock); d.Decision == "block" {
				if err := blogwatch.Append(path, blogwatch.Decision{
					URL: postURL, Source: source, Verdict: blogwatch.VerdictDeclined,
					Reason: "sanitization: " + d.Reason, RecordedAt: blogWatchNow(),
				}); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "blog-watch screen: blocked %s (%s); recorded Declined\n", postURL, d.Reason)
				return verdictExit{code: exitBlogWatchScreened}
			}
			return os.WriteFile(out, []byte(text), 0o600)
		},
	}
	cmd.Flags().StringVar(&postURL, "url", "", "Post URL — required")
	cmd.Flags().StringVar(&out, "out", "", "File to write the screened text to — required")
	cmd.Flags().StringVar(&source, "source", "", "Source name, recorded on a Declined row")
	return cmd
}

func blogWatchRecordCmd(env *blogWatchEnv) *cobra.Command {
	var d blogwatch.Decision
	var verdict string
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Append one verdict to the decision log",
		Long: `Appends one validated row. --verdict must be one of the five verdicts;
--reason is always required; "Already covered" needs --ref; "Already filed"
needs --issue. A URL that already has a verdict is refused. Use adopt, not
record, to file an issue.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := blogwatch.ParseVerdict(verdict)
			if err != nil {
				return err
			}
			if v == blogwatch.VerdictAdopted {
				return fmt.Errorf("use `blog-watch adopt` to record Adopted; it files the issue")
			}
			path, err := env.logFile()
			if err != nil {
				return err
			}
			d.Verdict, d.RecordedAt = v, blogWatchNow()
			if err := blogwatch.Append(path, d); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s: %s\n", v, d.URL)
			return nil
		},
	}
	cmd.Flags().StringVar(&d.URL, "url", "", "Post URL — required")
	cmd.Flags().StringVar(&verdict, "verdict", "", "Adopted | Already covered | Already filed | Declined | Deferred")
	cmd.Flags().StringVar(&d.Reason, "reason", "", "Why — required")
	cmd.Flags().StringVar(&d.Ref, "ref", "", "Doc or code that covers it (Already covered)")
	cmd.Flags().IntVar(&d.Issue, "issue", 0, "Existing issue number (Already filed)")
	cmd.Flags().StringVar(&d.Source, "source", "", "Source name")
	return cmd
}

func blogWatchAdoptCmd(env *blogWatchEnv) *cobra.Command {
	var postURL, title, gapFile, excerpt, source, reason, owner, repo string
	var project int
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "File the issue for an Adopted post and record it",
		Long: `Refuses a URL that already has a verdict before any forge call. Builds the
body (our gap first, at most one fenced excerpt, a Trigger line) and the closed
label set type:feature + area:blog-watch, files the issue in Backlog, and
records Adopted #N.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if postURL == "" || title == "" || gapFile == "" {
				return fmt.Errorf("--url, --title and --gap-file are required")
			}
			norm, ok := blogwatch.Normalize(postURL)
			if !ok {
				return fmt.Errorf("--url %q is not an absolute https URL", postURL)
			}
			path, err := env.logFile()
			if err != nil {
				return err
			}
			log, err := blogwatch.LoadLog(path)
			if err != nil {
				return err
			}
			if blogwatch.RecordedURLs(log)[norm] {
				return &blogwatch.DuplicateError{URL: norm}
			}
			gap, err := os.ReadFile(gapFile)
			if err != nil {
				return err
			}
			body, err := blogwatch.BuildIssueBody(string(gap), excerpt, norm)
			if err != nil {
				return err
			}
			filer, err := newBlogWatchFiler(cmd, owner, repo, project)
			if err != nil {
				return err
			}
			n, err := filer.Create(cmd.Context(), title, body, blogwatch.IssueLabels())
			if n > 0 {
				if reason == "" {
					reason = "gap: " + firstLine(string(gap))
				}
				if aerr := blogwatch.Append(path, blogwatch.Decision{
					URL: norm, Source: source, Verdict: blogwatch.VerdictAdopted,
					Reason: reason, Issue: n, RecordedAt: blogWatchNow(),
				}); aerr != nil {
					err = errors.Join(err, aerr)
				}
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "adopted %s as #%d\n", norm, n)
			return nil
		},
	}
	ownerFlag(cmd, &owner, "GitHub repository owner")
	requiredRepoNameFlag(cmd, &repo, "GitHub repository name")
	cmd.Flags().IntVar(&project, "project", 0, "Project board number (defaults to config)")
	cmd.Flags().StringVar(&postURL, "url", "", "Post URL — required")
	cmd.Flags().StringVar(&title, "title", "", "Issue title — required")
	cmd.Flags().StringVar(&gapFile, "gap-file", "", "File holding our measured gap, in our words — required")
	cmd.Flags().StringVar(&excerpt, "excerpt", "", "One excerpt from the post (capped, fenced)")
	cmd.Flags().StringVar(&reason, "reason", "", "Log reason (default: the gap's first line)")
	cmd.Flags().StringVar(&source, "source", "", "Source name")
	return cmd
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	return s
}

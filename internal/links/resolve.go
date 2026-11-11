package links

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Kinds of reference ParseRef knows.
const (
	KindRequest   = "request"
	KindCriterion = "criterion"
	KindTask      = "task"
	KindPain      = "pain"
	KindIdea      = "idea"
	KindFeature   = "feature"
	KindDecision  = "decision"
	KindKnowledge = "knowledge"
	KindDocument  = "document"
	KindRoom      = "room"
	KindSession   = "session"
	KindPackage   = "package"
	KindBranch    = "branch"
	KindRunbook   = "runbook"
	KindPreset    = "preset"
	KindCommit    = "commit"
	KindPull      = "pull"
	KindPath      = "path"
	KindQuery     = "query"
	KindInbox     = "inbox"
)

// Ref is a parsed short form: REQ-596, TASK-124, PAIN-57, IDEA-3, FEAT-4, KN-4295,
// pr:155, commit:2d950bf, preset:pr-ergebnis 155, a path, a search line.
type Ref struct {
	Kind string
	ID   int64    // numbers: REQ, TASK, PAIN, IDEA, FEAT, PKG, knowledge, room, pr
	Num  int      // criterion number
	Text string   // name, hash, path, branch, search line, session address
	Args []string // positional values of a preset
	// Project is the repository a reference names itself (doc:owner/repo/slug),
	// as a normalized remote; empty means the current project.
	Project string
}

var (
	idRE        = regexp.MustCompile(`(?i)^(req|task|pain|idea|feat|pkg|dec|kn)-(\d+)$`)
	criterionRE = regexp.MustCompile(`(?i)^(?:req-(\d+)#(\d+)|ac-(\d+)\.(\d+))$`)
	hashRE      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	prefixRE    = regexp.MustCompile(`^([a-z]+):(.*)$`)
	nameRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)
)

// ParseRef reads one short form. The input may be several words (a preset with
// its values, a search line); the caller joins its arguments with blanks.
func ParseRef(input string) (Ref, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Ref{}, errors.New("name what to open: REQ-n, TASK-n, PAIN-n, IDEA-n, FEAT-n, DEC-n, KN-n, pr:n, commit:sha, preset:name, a path or a search line")
	}
	atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	num := func(s string) int { n, _ := strconv.ParseInt(s, 10, 32); return int(n) }
	if m := criterionRE.FindStringSubmatch(in); m != nil {
		if m[1] != "" {
			return Ref{Kind: KindCriterion, ID: atoi(m[1]), Num: num(m[2])}, nil
		}
		return Ref{Kind: KindCriterion, ID: atoi(m[3]), Num: num(m[4])}, nil
	}
	if t, ok := ParseNumber(in); ok {
		// A bare #N given as a reference (ctx open #4295) is the old way of writing a
		// knowledge entry, now KN-N; in running text of a repository #N is the pull
		// request. A search asks NumberTerm.Kinds for every kind #N can mean.
		if t.Kind == "" {
			return Ref{Kind: KindKnowledge, ID: t.N}, nil
		}
		return Ref{Kind: t.Kind, ID: t.N}, nil
	}
	if m := idRE.FindStringSubmatch(in); m != nil {
		kind := map[string]string{"req": KindRequest, "task": KindTask, "pain": KindPain, "idea": KindIdea, "feat": KindFeature, "pkg": KindPackage, "dec": KindDecision, "kn": KindKnowledge}[strings.ToLower(m[1])]
		return Ref{Kind: kind, ID: atoi(m[2])}, nil
	}
	// #4295 is the old spelling of KN-4295 and is still read.
	if strings.HasPrefix(in, "#") {
		n, ok := knref.Parse(in)
		if !ok {
			return Ref{}, fmt.Errorf("%q is not a knowledge number like KN-4295", in)
		}
		return Ref{Kind: KindKnowledge, ID: n}, nil
	}
	if strings.EqualFold(in, "inbox") {
		return Ref{Kind: KindInbox}, nil
	}
	if m := prefixRE.FindStringSubmatch(in); m != nil {
		prefix, rest := m[1], strings.TrimSpace(m[2])
		fields := strings.Fields(rest)
		first := ""
		if len(fields) > 0 {
			first = fields[0]
		}
		num := func() (int64, error) {
			n, err := strconv.ParseInt(strings.TrimPrefix(first, "#"), 10, 64)
			if err != nil || n <= 0 || len(fields) != 1 {
				return 0, fmt.Errorf("%s: needs one number, for example %s:155", prefix, prefix)
			}
			return n, nil
		}
		switch prefix {
		case "pr", "pull":
			n, err := num()
			return Ref{Kind: KindPull, ID: n}, err
		case "commit", "sha":
			if !hashRE.MatchString(strings.ToLower(first)) || len(fields) != 1 {
				return Ref{}, fmt.Errorf("commit: needs 7 to 40 hexadecimal characters")
			}
			return Ref{Kind: KindCommit, Text: strings.ToLower(first)}, nil
		case "knowledge", "k":
			n, err := num()
			return Ref{Kind: KindKnowledge, ID: n}, err
		case "room", "thread":
			n, err := num()
			return Ref{Kind: KindRoom, ID: n}, err
		case "session":
			if first == "" || len(fields) != 1 {
				return Ref{}, errors.New("session: needs the session number or address")
			}
			return Ref{Kind: KindSession, Text: first}, nil
		case "doc", "document":
			repo, slug := "", first
			if i := strings.LastIndex(first, "/"); i >= 0 {
				repo, slug = first[:i], first[i+1:]
			}
			project := docRepo(repo)
			if !nameRE.MatchString(slug) || len(fields) != 1 || (repo != "" && project == "") {
				return Ref{}, errors.New("doc: needs the document name (slug), optionally after its repository: doc:owner/repo/slug")
			}
			return Ref{Kind: KindDocument, Text: slug, Project: project}, nil
		case "runbook":
			if !nameRE.MatchString(first) || len(fields) != 1 {
				return Ref{}, errors.New("runbook: needs the runbook name")
			}
			return Ref{Kind: KindRunbook, Text: first}, nil
		case "branch", "package", "checklist":
			if rest == "" || strings.ContainsAny(rest, " \t\n") {
				return Ref{}, fmt.Errorf("%s: needs a branch name", prefix)
			}
			return Ref{Kind: KindBranch, Text: rest}, nil
		case "preset":
			if !nameRE.MatchString(first) {
				return Ref{}, errors.New("preset: needs the name of a saved search, then its values")
			}
			return Ref{Kind: KindPreset, Text: first, Args: fields[1:]}, nil
		case "path":
			if rest == "" {
				return Ref{}, errors.New("path: needs a path")
			}
			return Ref{Kind: KindPath, Text: rest}, nil
		}
		// An unknown prefix is a qualifier of the search language (is:open ...).
		return Ref{Kind: KindQuery, Text: in}, nil
	}
	if strings.ContainsAny(in, " \t") {
		return Ref{Kind: KindQuery, Text: in}, nil
	}
	if strings.Contains(in, "/") || strings.Contains(in, ".") {
		return Ref{Kind: KindPath, Text: in}, nil
	}
	return Ref{}, fmt.Errorf("cannot tell what %q is: use REQ-n, TASK-n, PAIN-n, IDEA-n, FEAT-n, DEC-n, KN-n, pr:n, commit:sha, preset:name, a path or a search line like is:open", in)
}

// docRepo reads the repository part of doc:owner/repo/slug: owner/repo on
// github.com, or host/owner/repo. Anything else is "".
func docRepo(repo string) string {
	repo = strings.ToLower(repo)
	parts := strings.Split(repo, "/")
	switch {
	case repo == "":
		return ""
	case len(parts) == 2:
		repo = "github.com/" + repo
	case len(parts) == 3 && strings.Contains(parts[0], "."):
	default:
		return ""
	}
	for _, p := range strings.Split(repo, "/") {
		if !segmentRE.MatchString(p) {
			return ""
		}
	}
	return scope.NormalizeRemote(repo)
}

// API is what Resolve reads. *client.Client satisfies it; every method answers
// as the caller's account, so an object the caller may not read is unknown.
type API interface {
	GetRequest(id int64) (requestdomain.Detail, error)
	GetHumanTask(id int64) (humantask.Task, error)
	GetPain(id int64) (pain.Detail, error)
	GetIdea(id int64) (idea.Idea, error)
	GetFeature(id int64) (feature.Feature, error)
	GetDecision(id int64) (idea.Decision, error)
	KnowledgeByID(id int64) (store.Knowledge, error)
	Documents(project, kind string, includeArchived bool) ([]store.Document, error)
	ThreadByID(id int64, agentExternalID string) (store.Thread, error)
	ListRunbooks(ax scope.Axes) ([]runbook.Summary, error)
	ListPresets(project string) ([]store.SearchPreset, error)
	GetPackage(id int64, full bool) (requestdomain.PackageDetail, error)
	Sessions(filter scope.Axes, limit int) ([]store.Session, error)
}

// Env is where a reference is resolved: the server, the current project and
// branch, the working copy for paths, and the coordination identity for rooms.
type Env struct {
	Builder  Builder
	API      API
	Project  string
	Branch   string
	RepoRoot string
	Agent    string
}

// Unknown is the one answer for an object that does not exist and for one the
// caller may not read.
func Unknown(what string) error {
	return fmt.Errorf("%s not found, or you may not read it", what)
}

// Resolve turns a reference into a card with its address. Objects with a
// title are read through the API, which is where the caller's rights apply.
func (e Env) Resolve(ref Ref) (Card, error) {
	b := e.Builder
	if !b.Valid() {
		return Card{}, errors.New("no server address configured: run ctx login first")
	}
	needProject := func() error {
		if !b.HasProject() {
			return errors.New("this folder belongs to no project: name one object with its number (REQ-n, ...) or run it inside a repository")
		}
		return nil
	}
	switch ref.Kind {
	case KindInbox:
		return Card{Kind: "Inbox", Title: "what waits for you", URL: b.Inbox()}.Sanitized(), nil
	case KindRequest, KindCriterion:
		d, err := e.API.GetRequest(ref.ID)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("REQ-%d", ref.ID))
		}
		if ref.Kind == KindCriterion {
			c, ok := b.CriterionCard(d, ref.Num)
			if !ok {
				return Card{}, Unknown(fmt.Sprintf("AC-%d.%d", ref.ID, ref.Num))
			}
			return c, nil
		}
		return b.RequestCard(d), nil
	case KindTask:
		t, err := e.API.GetHumanTask(ref.ID)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("TASK-%d", ref.ID))
		}
		return b.TaskCard(t), nil
	case KindPain:
		d, err := e.API.GetPain(ref.ID)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("PAIN-%d", ref.ID))
		}
		return b.PainCard(d.Pain), nil
	case KindIdea:
		i, err := e.API.GetIdea(ref.ID)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("IDEA-%d", ref.ID))
		}
		return b.IdeaCard(i), nil
	case KindFeature:
		f, err := e.API.GetFeature(ref.ID)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("FEAT-%d", ref.ID))
		}
		return b.FeatureCard(f), nil
	case KindDecision:
		d, err := e.API.GetDecision(ref.ID)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("DEC-%d", ref.ID))
		}
		return b.DecisionCard(d), nil
	case KindKnowledge:
		k, err := e.API.KnowledgeByID(ref.ID)
		if err != nil {
			return Card{}, Unknown(knref.Format(ref.ID))
		}
		return b.fallbackProject(e.Project).KnowledgeCard(k), nil
	case KindPackage:
		p, err := e.API.GetPackage(ref.ID, false)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("PKG-%d", ref.ID))
		}
		return b.PackageCard(p), nil
	case KindRoom:
		t, err := e.API.ThreadByID(ref.ID, e.Agent)
		if err != nil {
			return Card{}, Unknown(fmt.Sprintf("room thread %d", ref.ID))
		}
		return b.fallbackProject(e.Project).ThreadCard(t, 0), nil
	case KindSession:
		list, err := e.API.Sessions(scope.Axes{Project: e.Project}, 200)
		if err == nil {
			for _, s := range list {
				if s.PublicID == ref.Text || num(s.ID) == ref.Text {
					return b.SessionCard(s), nil
				}
			}
		}
		return Card{}, Unknown("session " + Clean(ref.Text, 40))
	case KindDocument:
		project := ref.Project
		if project == "" {
			if err := needProject(); err != nil {
				return Card{}, err
			}
			project = e.Project
		}
		docs, err := e.API.Documents(project, "", true)
		if err == nil {
			for _, d := range docs {
				if d.Slug == ref.Text {
					return b.DocumentCard(d), nil
				}
			}
		}
		return Card{}, Unknown("document " + Clean(ref.Text, 60))
	case KindRunbook:
		if err := needProject(); err != nil {
			return Card{}, err
		}
		list, err := e.API.ListRunbooks(scope.Axes{Project: e.Project})
		if err == nil {
			for _, s := range list {
				if s.Name == ref.Text {
					return b.fallbackProject(e.Project).RunbookCard(s), nil
				}
			}
		}
		return Card{}, Unknown("runbook " + Clean(ref.Text, 60))
	case KindPreset:
		return e.resolvePreset(ref)
	case KindPull:
		if err := needProject(); err != nil {
			return Card{}, err
		}
		return Card{Kind: "Pull request", Ref: "#" + num(ref.ID), Next: "none", URL: b.Pull(ref.ID)}, nil
	case KindCommit:
		if err := needProject(); err != nil {
			return Card{}, err
		}
		return Card{Kind: "Commit", Ref: ref.Text, Next: "none", URL: b.Commit(ref.Text)}, nil
	case KindBranch:
		if err := needProject(); err != nil {
			return Card{}, err
		}
		return Card{Kind: "Checklist", Ref: "branch " + ref.Text, Next: "none", URL: b.Package(ref.Text)}.Sanitized(), nil
	case KindPath:
		if err := needProject(); err != nil {
			return Card{}, err
		}
		return e.resolvePath(ref.Text)
	case KindQuery:
		u := b.Search(ref.Text)
		if b.HasProject() {
			u = b.Issues(ref.Text)
		}
		return Card{Kind: "Search", Title: ref.Text, Next: "none", URL: u}.Sanitized(), nil
	}
	return Card{}, fmt.Errorf("cannot open a %q", ref.Kind)
}

// fallbackProject uses the current project for an object that has none of its
// own (global knowledge): the page then is the one of the project asked from.
func (b Builder) fallbackProject(project string) Builder {
	if b.project == "" {
		return b.WithProject(project)
	}
	return b
}

func (e Env) resolvePreset(ref Ref) (Card, error) {
	if e.Project == "" {
		return Card{}, errors.New("this folder belongs to no project: run it inside a repository")
	}
	list, err := e.API.ListPresets(e.Project)
	if err != nil {
		return Card{}, Unknown("preset " + Clean(ref.Text, 60))
	}
	for _, p := range list {
		if p.Name != ref.Text {
			continue
		}
		args := map[string]string{}
		if len(ref.Args) > len(p.Args) {
			return Card{}, fmt.Errorf("preset %s takes %d value(s): %s", p.Name, len(p.Args), strings.Join(p.Args, ", "))
		}
		for i, v := range ref.Args {
			args[p.Args[i]] = v
		}
		return e.Builder.PresetCard(p, args), nil
	}
	return Card{}, Unknown("preset " + Clean(ref.Text, 60))
}

// resolvePath maps a file or folder to its page on the current branch. A path
// outside the working copy is refused: it names nothing of the project.
func (e Env) resolvePath(p string) (Card, error) {
	rel := p
	if filepath.IsAbs(p) {
		if e.RepoRoot == "" {
			return Card{}, errors.New("an absolute path needs a repository to be relative to")
		}
		r, err := filepath.Rel(e.RepoRoot, p)
		if err != nil {
			return Card{}, fmt.Errorf("%s is outside the repository", Clean(p, 80))
		}
		rel = r
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return Card{}, fmt.Errorf("%s is outside the repository", Clean(p, 80))
	}
	if rel == "." {
		rel = ""
	}
	branch := e.Branch
	if branch == "" {
		branch = "main"
	}
	dir := false
	if e.RepoRoot != "" {
		if info, err := os.Stat(filepath.Join(e.RepoRoot, filepath.FromSlash(rel))); err == nil {
			dir = info.IsDir()
		} else if strings.HasSuffix(p, "/") {
			dir = true
		}
	}
	if rel == "" {
		return Card{Kind: "Folder", Ref: branch, Next: "none", URL: e.Builder.Tree(branch, "")}.Sanitized(), nil
	}
	if dir {
		return Card{Kind: "Folder", Ref: rel, Title: "on " + branch, Next: "none", URL: e.Builder.Tree(branch, rel)}.Sanitized(), nil
	}
	return Card{Kind: "File", Ref: rel, Title: "on " + branch, Next: "none", URL: e.Builder.Blob(branch, rel)}.Sanitized(), nil
}

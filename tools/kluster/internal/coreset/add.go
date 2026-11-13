package coreset

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// AddRequest names a project to register in the files the check reads, and
// optionally one component's row in the deploy catalog.
type AddRequest struct {
	Project string
	// Component adds the row <project>-<component> to the deploy catalog;
	// "" adds none.
	Component string
	// Repo is the component's app repository, owner/name; "" writes null,
	// which resolves no version.
	Repo      string
	TagPrefix string
	// Host is the component's public host; "" leaves the field out.
	Host string
}

// FileEdit is one file a registration writes. Old is nil for a new file.
type FileEdit struct {
	Path     string
	Old, New []byte
}

// Registration is what AddProject would write.
type Registration struct {
	Edits []FileEdit
	// Standing are the check's refusals that were there before and stay:
	// not the registration's to fix.
	Standing []error
}

// dnsLabel is a valid namespace name (RFC 1123 label).
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// AddProject returns the edits that register a project everywhere the
// check asks for it: the projects generator, its AppProject, the core
// AppProject's destinations, both Gateway listeners, its LimitRange
// directory and listing, its namespace file and, for a component, the deploy
// catalog row. It adds only what is missing, so a registered project gives no
// edit. It edits the text at the lines the YAML nodes name, so every comment
// stays, and it refuses a result that adds a refusal to services check.
//
// The component's own files (base, overlay, Service, route, Probe target)
// are not written: they differ for each application (the manifests rule).
func AddProject(fsys fs.FS, req AddRequest) (Registration, error) {
	if err := validate(fsys, req); err != nil {
		return Registration{}, err
	}
	before := Check(fsys)
	w := newOverlay(fsys)
	steps := []func(*overlay, AddRequest) error{
		addGeneratorEntry,
		addAppProject,
		addCoreDestination,
		admitOnListeners,
		listLimitRange,
		writeLimitRange,
		writeNamespace,
		addCatalogRow,
	}
	for _, step := range steps {
		if err := step(w, req); err != nil {
			return Registration{}, err
		}
	}
	standing, added := splitRefusals(before, Check(w))
	if len(added) > 0 {
		return Registration{}, fmt.Errorf("the registration of %s does not pass services check:\n%w", req.Project, errors.Join(added...))
	}
	return Registration{Edits: w.edits(), Standing: standing}, nil
}

// validate refuses a name that cannot be a namespace, or that a core
// directory or Kubernetes already uses.
func validate(fsys fs.FS, req AddRequest) error {
	if !dnsLabel.MatchString(req.Project) {
		return fmt.Errorf("project %q is not a namespace name (lower case, digits and -, at most 63)", req.Project)
	}
	if req.Component != "" && !dnsLabel.MatchString(req.Component) {
		return fmt.Errorf("component %q is not a lower-case name of letters, digits and -", req.Component)
	}
	if req.Project == "default" || strings.HasPrefix(req.Project, "kube-") {
		return fmt.Errorf("project %q: Kubernetes owns that namespace", req.Project)
	}
	projects, err := Projects(fsys)
	if err != nil {
		return err
	}
	if slices.Contains(projects, req.Project) {
		return nil
	}
	core, err := Core(fsys)
	if err != nil {
		return err
	}
	dests, err := destinations(fsys, "core")
	if err != nil {
		return err
	}
	for _, e := range core {
		dests = append(dests, e.Namespace)
	}
	if slices.Contains(dests, req.Project) {
		return fmt.Errorf("project %q: a core directory already writes to that namespace", req.Project)
	}
	return nil
}

// splitRefusals sorts the check's refusals after an edit into those that
// were there before and those the edit adds.
func splitRefusals(before, after []error) (standing, added []error) {
	was := map[string]bool{}
	for _, e := range before {
		was[e.Error()] = true
	}
	for _, e := range after {
		if was[e.Error()] {
			standing = append(standing, e)
		} else {
			added = append(added, e)
		}
	}
	return standing, added
}

// ---------- the steps ----------

// addGeneratorEntry lists the project in the projects generator.
func addGeneratorEntry(w *overlay, req AddRequest) error {
	projects, err := Projects(w)
	if err != nil || slices.Contains(projects, req.Project) {
		return err
	}
	git, err := projectsGenerator(w)
	if err != nil {
		return err
	}
	dirs := lookup(git, "directories")
	return w.editSequence(ProjectsAppSetFile, dirs, len(dirs.Content)-1,
		"path: projects/"+req.Project+"/*/"+OverlaySuffix)
}

// addAppProject writes the project's AppProject after the last project's,
// in the same shape: this repository only, its own namespace only, no
// cluster-scoped kind.
func addAppProject(w *overlay, req AddRequest) error {
	if _, err := find(w, AppProjectsFile, "AppProject", req.Project); err == nil {
		return nil
	}
	git, err := projectsGenerator(w)
	if err != nil {
		return err
	}
	repoURL := lookup(git, "repoURL")
	if repoURL == nil {
		return fmt.Errorf("%s: the git generator has no repoURL", ProjectsAppSetFile)
	}
	projects, err := Projects(w)
	if err != nil {
		return err
	}
	objs, err := Objects(w, AppProjectsFile)
	if err != nil {
		return err
	}
	var last *Object
	for i, o := range objs {
		if o.Kind == "AppProject" && o.Metadata.Name != req.Project && slices.Contains(projects, o.Metadata.Name) {
			last = &objs[i]
		}
	}
	if last == nil {
		return fmt.Errorf("%s: no project AppProject to place %s after", AppProjectsFile, req.Project)
	}
	doc := []string{
		"---",
		"apiVersion: argoproj.io/v1alpha1",
		"kind: AppProject",
		"metadata:",
		"  name: " + req.Project,
		"  namespace: argocd",
		"spec:",
		"  description: " + req.Project + " — its components, all in the " + req.Project + " namespace",
		"  sourceRepos:",
		"    - " + repoURL.Value,
		"  destinations:",
		"    - server: https://kubernetes.default.svc",
		"      namespace: " + req.Project,
		"  clusterResourceWhitelist: []",
		"  namespaceResourceWhitelist:",
		"    - group: '*'",
		"      kind: '*'",
	}
	data, err := fs.ReadFile(w, AppProjectsFile)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// Before the separator that ends the last project's document, or at
	// the end of the file.
	at := len(lines)
	if lines[at-1] == "" {
		at--
	}
	for i := lastLine(last.Node); i < len(lines); i++ {
		if lines[i] == "---" {
			at = i
			break
		}
	}
	w.write(AppProjectsFile, joinLines(slices.Insert(lines, at, doc...)))
	return nil
}

// addCoreDestination admits the namespace in the core AppProject, after the
// last project namespace there: core-limit-ranges writes its LimitRange.
func addCoreDestination(w *overlay, req AddRequest) error {
	dests, err := destinations(w, "core")
	if err != nil || slices.Contains(dests, req.Project) {
		return err
	}
	o, err := find(w, AppProjectsFile, "AppProject", "core")
	if err != nil {
		return err
	}
	seq := lookup(o.Node, "spec", "destinations")
	projects, err := Projects(w)
	if err != nil {
		return err
	}
	at := lastProjectItem(seq, projects, req.Project, func(n *yaml.Node) string { return valueOf(lookup(n, "namespace")) })
	server := valueOf(lookup(seq.Content[at], "server"))
	if server == "" {
		server = "https://kubernetes.default.svc"
	}
	return w.editSequence(AppProjectsFile, seq, at, fmt.Sprintf("{server: %s, namespace: %s}", server, req.Project))
}

// admitOnListeners adds the namespace to the allowedRoutes selector of each
// Gateway listener that lacks it.
func admitOnListeners(w *overlay, req AddRequest) error {
	projects, err := Projects(w)
	if err != nil {
		return err
	}
	// One listener per pass: an edit can move the lines of the next.
	for {
		values, err := listenerLacking(w, req.Project)
		if err != nil || values == nil {
			return err
		}
		at := lastProjectItem(values, projects, req.Project, valueOf)
		if err := w.editSequence(GatewayFile, values, at, req.Project); err != nil {
			return err
		}
	}
}

// listenerLacking returns the selector values of the first listener that
// does not admit ns, or nil when all do.
func listenerLacking(w *overlay, ns string) (*yaml.Node, error) {
	o, err := find(w, GatewayFile, "Gateway", "tosak")
	if err != nil {
		return nil, err
	}
	listeners := lookup(o.Node, "spec", "listeners")
	if listeners == nil {
		return nil, fmt.Errorf("%s: the Gateway has no listener", GatewayFile)
	}
	for _, l := range listeners.Content {
		var values *yaml.Node
		exprs := lookup(l, "allowedRoutes", "namespaces", "selector", "matchExpressions")
		if exprs != nil {
			for _, e := range exprs.Content {
				if valueOf(lookup(e, "key")) == "kubernetes.io/metadata.name" && valueOf(lookup(e, "operator")) == "In" {
					values = lookup(e, "values")
				}
			}
		}
		if values == nil || values.Kind != yaml.SequenceNode || len(values.Content) == 0 {
			return nil, fmt.Errorf("%s: listener %s has no kubernetes.io/metadata.name In [...] selector; admit %s by hand",
				GatewayFile, valueOf(lookup(l, "name")), ns)
		}
		if !slices.ContainsFunc(values.Content, func(n *yaml.Node) bool { return n.Value == ns }) {
			return values, nil
		}
	}
	return nil, nil
}

// listLimitRange lists the project's LimitRange directory.
func listLimitRange(w *overlay, req AddRequest) error {
	file := LimitRangesDir + "/kustomization.yaml"
	objs, err := Objects(w, file)
	if err != nil {
		return err
	}
	if len(objs) != 1 {
		return fmt.Errorf("%s: want one document", file)
	}
	seq := lookup(objs[0].Node, "resources")
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		return fmt.Errorf("%s: no resources list to add %s to", file, req.Project)
	}
	if slices.ContainsFunc(seq.Content, func(n *yaml.Node) bool { return n.Value == req.Project }) {
		return nil
	}
	return w.editSequence(file, seq, len(seq.Content)-1, req.Project)
}

// writeLimitRange writes core/limit-ranges/<project>: the base with the
// project's namespace.
func writeLimitRange(w *overlay, req AddRequest) error {
	dir := LimitRangesDir + "/" + req.Project
	if _, err := fs.Stat(w, dir+"/kustomization.yaml"); err == nil {
		return nil // the check names a wrong namespace; it is not overwritten
	}
	w.write(dir+"/kustomization.yaml", []byte(
		"apiVersion: kustomize.config.k8s.io/v1beta1\n"+
			"kind: Kustomization\n"+
			"namespace: "+req.Project+"\n"+
			"resources:\n"+
			"  - ../base\n"))
	return nil
}

// writeNamespace writes projects/<project>/namespace.yaml, which the seed
// applies.
func writeNamespace(w *overlay, req AddRequest) error {
	file := "projects/" + req.Project + "/namespace.yaml"
	if _, err := fs.Stat(w, file); err == nil {
		return nil // the check names a file without the Namespace
	}
	p := req.Project
	w.write(file, []byte(
		"# The namespace of "+p+". The kluster seed applies it (`services seed`), not\n"+
			"# ArgoCD: the "+p+" AppProject admits no cluster-scoped kind\n"+
			"# (core/argocd/appprojects.yaml).\n"+
			"#\n"+
			"# The name equals the project's directory, its AppProject and the prefix of\n"+
			"# its Applications (core/argocd/applicationset.yaml); both listeners of\n"+
			"# core/gateway/gateway.yaml admit routes from it. `kluster project add`\n"+
			"# wrote these entries; `kluster services check` keeps them in step.\n"+
			"apiVersion: v1\n"+
			"kind: Namespace\n"+
			"metadata:\n"+
			"  name: "+p+"\n"))
	return nil
}

// addCatalogRow adds the component's row to the deploy catalog, after the
// project's other rows or at the end. The catalog holds one row per line.
func addCatalogRow(w *overlay, req AddRequest) error {
	if req.Component == "" {
		return nil
	}
	key := req.Project + "-" + req.Component
	apps, err := appNames(w)
	if err != nil || apps[key] {
		return err
	}
	data, err := fs.ReadFile(w, AppsFile)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	end := slices.IndexFunc(lines, func(l string) bool { return l == "}" })
	if end < 1 {
		return fmt.Errorf("%s: no closing } on a line of its own", AppsFile)
	}
	after := end - 1
	for i := 1; i < end; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), `"`+req.Project+"-") {
			after = i
		}
	}
	row := catalogRow(key, req)
	switch {
	case after < end-1:
		row += ","
	case strings.TrimSpace(lines[after]) != "{" && !strings.HasSuffix(lines[after], ","):
		lines[after] += ","
	}
	out := joinLines(slices.Insert(lines, after+1, row))
	var check map[string]json.RawMessage
	if err := json.Unmarshal(out, &check); err != nil {
		return fmt.Errorf("%s: the row for %s would break the JSON (%w); add it by hand", AppsFile, key, err)
	}
	w.write(AppsFile, out)
	return nil
}

func catalogRow(key string, req AddRequest) string {
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	repo := "null"
	if req.Repo != "" {
		repo = quote(req.Repo)
	}
	row := fmt.Sprintf(`  %s: { "repo": %s, "tagPrefix": %s`, quote(key), repo, quote(req.TagPrefix))
	if req.Host != "" {
		row += `, "host": ` + quote(req.Host)
	}
	return row + " }"
}

// ---------- text edits at the lines the nodes name ----------

// editSequence adds item to the YAML sequence seq of file, after the item at
// index at. A block sequence gets a new line in the indentation of its
// items; a flow sequence of plain scalars on one line is written again with
// the item in it. item is one line of YAML.
func (w *overlay) editSequence(file string, seq *yaml.Node, at int, item string) error {
	data, err := fs.ReadFile(w, file)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	if seq.Style&yaml.FlowStyle != 0 {
		line, err := withFlowItem(lines[seq.Line-1], seq, at, item)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, seq.Line, err)
		}
		lines[seq.Line-1] = line
	} else {
		prev := seq.Content[at]
		indent := prev.Column - 3 // the item follows "- "
		l := lines[prev.Line-1]
		if indent < 0 || len(l) < indent+2 || l[indent:indent+2] != "- " {
			return fmt.Errorf("%s:%d: the list item is not on a \"- \" line; add %s by hand", file, prev.Line, item)
		}
		lines = slices.Insert(lines, lastLine(prev), l[:indent]+"- "+item)
	}
	w.write(file, joinLines(lines))
	return nil
}

// withFlowItem writes a one-line flow sequence of plain scalars again, with
// item after the item at index at.
func withFlowItem(line string, seq *yaml.Node, at int, item string) (string, error) {
	start := seq.Column - 1
	if lastLine(seq) != seq.Line || start >= len(line) || line[start] != '[' {
		return "", errors.New("the flow list is not on one line")
	}
	end := strings.IndexByte(line[start:], ']')
	if end < 0 {
		return "", errors.New("the flow list does not end on its line")
	}
	values := make([]string, 0, len(seq.Content)+1)
	for _, n := range seq.Content {
		if n.Kind != yaml.ScalarNode || n.Style != 0 {
			return "", fmt.Errorf("item %q is not a plain scalar", n.Value)
		}
		values = append(values, n.Value)
	}
	values = slices.Insert(values, at+1, item)
	return line[:start] + "[" + strings.Join(values, ", ") + "]" + line[start+end+1:], nil
}

// lastProjectItem returns the index of the last item of seq that names a
// listed project other than skip, or the last item when none does. A new
// project goes after it, so the projects stay together.
func lastProjectItem(seq *yaml.Node, projects []string, skip string, name func(*yaml.Node) string) int {
	at := len(seq.Content) - 1
	for i, n := range seq.Content {
		if v := name(n); v != skip && slices.Contains(projects, v) {
			at = i
		}
	}
	return at
}

// projectsGenerator returns the git generator of the projects ApplicationSet.
func projectsGenerator(fsys fs.FS) (*yaml.Node, error) {
	o, err := find(fsys, ProjectsAppSetFile, "ApplicationSet", "projects")
	if err != nil {
		return nil, err
	}
	gens := lookup(o.Node, "spec", "generators")
	if gens == nil || len(gens.Content) != 1 || lookup(gens.Content[0], "git") == nil {
		return nil, fmt.Errorf("%s: want exactly one git generator", ProjectsAppSetFile)
	}
	return lookup(gens.Content[0], "git"), nil
}

// lookup follows keys through nested mappings; nil when one is missing.
func lookup(n *yaml.Node, keys ...string) *yaml.Node {
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	for _, k := range keys {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == k {
				next = n.Content[i+1]
			}
		}
		n = next
	}
	return n
}

func valueOf(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

// lastLine is the last line (1-based) that n or a node inside it starts on.
func lastLine(n *yaml.Node) int {
	last := n.Line
	for _, c := range n.Content {
		last = max(last, lastLine(c))
	}
	return last
}

func joinLines(lines []string) []byte { return []byte(strings.Join(lines, "\n")) }

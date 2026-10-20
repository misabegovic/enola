package rubyextractor

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/enola-labs/enola/internal/extractors/extcoverage"
	"github.com/enola-labs/enola/internal/facts"
)

// A Rails app's deploy files say what runs it in production: the processes a
// Procfile starts, the queues a worker drains, the periodic jobs a schedule
// enqueues, the roles a deploy tool places on hosts. Each becomes a
// runtime_unit whose runs edges name the job classes its entrypoint reaches, so
// "which process runs this file, on what schedule" is a walk rather than four
// files opened by hand. What a file computes (ERB, an environment variable) is
// recorded as unresolved, never guessed.

const (
	unitProcess   = "process"
	unitWorker    = "worker"
	unitSchedule  = "schedule"
	unitContainer = "container"
	unitRole      = "role"
	unitAccessory = "accessory"

	entryWeb      = "web"
	entryJobClass = "job_class"
	entryRunner   = "runner"
	entryRake     = "rake_task"
	entryScript   = "script"
	entryCommand  = "command"
	entryQueues   = "queues"

	// kamalPlaceholderHost is the address `kamal init` writes, which nobody
	// deploys to: a role still naming it says where the app does not run yet.
	kamalPlaceholderHost = "192.168.0.1"

	allQueues    = "*"
	defaultQueue = "default"
	erbToken     = "__erb__"
)

var (
	reERB          = regexp.MustCompile(`(?s)<%.*?%>`)
	reProcfileLine = regexp.MustCompile(`^([A-Za-z0-9_.-]+):\s*(.+)$`)
	reWebCommand   = regexp.MustCompile(`\b(puma|unicorn|passenger|thrust|rackup)\b|\brails\s+(server|s)\b`)
	reSidekiq      = regexp.MustCompile(`\bsidekiq\b`)
	reSolidQueue   = regexp.MustCompile(`(^|[\s/])bin/jobs\b|solid_queue:start`)
	reConfigFlag   = regexp.MustCompile(`(?:-C|--config)[ =](\S+)`)
	reQueueEnv     = regexp.MustCompile(`\b[A-Z0-9_]*QUEUE(?:_NAMES|S)=(\S+)`)
	reEnvAssign    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
	reSolidQueueV  = regexp.MustCompile(`(?m)^\s+solid_queue \((\d+)\.(\d+)`)
	reWheneverEach = regexp.MustCompile(`^every\s+(.+?)\s+do\s*$`)
	reWheneverJob  = regexp.MustCompile(`^(runner|rake|command)\s+["'](.+)["']`)
	reLeadingConst = regexp.MustCompile(`^([A-Z][A-Za-z0-9_]*(?:::[A-Z][A-Za-z0-9_]*)*)`)
	reTOMLTable    = regexp.MustCompile(`^\[([A-Za-z0-9_.-]+)\]\s*$`)
	reTOMLPair     = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*(.+?)\s*$`)
	reDockerCMD    = regexp.MustCompile(`(?i)^CMD\s+(.+)$`)
)

// deployUnit is one unit as read, before its runs edges are resolved.
type deployUnit struct {
	file, name string
	line       int
	props      map[string]any
	queues     []string
	class      string
}

// jobIndex is what the repository's facts already say about its job classes.
type jobIndex struct {
	classes  map[string]bool
	byQueue  map[string][]string
	jobs     []string
	unqueued []string
}

// newJobIndex indexes the job classes of one app root: a fact belongs to the
// deepest root holding its file, so a worker in one app never runs another's
// classes, as it cannot in production.
func newJobIndex(allFacts []facts.Fact, root string, roots []string) jobIndex {
	idx := jobIndex{classes: map[string]bool{}, byQueue: map[string][]string{}}
	queued := map[string]bool{}
	jobs := map[string]bool{}
	for _, f := range allFacts {
		if ownerRoot(f.File, roots) != root {
			continue
		}
		switch {
		case f.Kind == facts.KindSymbol && f.PropString("symbol_kind") == facts.SymbolClass:
			idx.classes[f.Name] = true
			if f.PropString("rails_component") == "job" {
				jobs[f.Name] = true
			}
		case f.Kind == facts.KindStorage && f.PropString(facts.PropSource) == facts.MessagingSourceRubyJobQueue:
			class := f.PropString("code_symbol")
			if class == "" {
				continue
			}
			idx.byQueue[f.Name] = append(idx.byQueue[f.Name], class)
			queued[class] = true
			jobs[class] = true
		}
	}
	for class := range jobs {
		idx.jobs = append(idx.jobs, class)
		if !queued[class] {
			idx.unqueued = append(idx.unqueued, class)
		}
	}
	sort.Strings(idx.jobs)
	sort.Strings(idx.unqueued)
	return idx
}

// drainedBy is every job class a worker on these queues runs. A class naming no
// queue is on the default queue, which is how both ActiveJob and Sidekiq read it.
func (idx jobIndex) drainedBy(queues []string) []string {
	seen := map[string]bool{}
	for _, q := range queues {
		var classes []string
		switch q {
		case allQueues:
			classes = idx.jobs
		case defaultQueue:
			classes = append(append(classes, idx.byQueue[q]...), idx.unqueued...)
		default:
			classes = idx.byQueue[q]
		}
		for _, c := range classes {
			seen[c] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// applyRuntimeUnits reads the deploy files of every Rails app in the
// repository and returns its runtime units and the reader's coverage account.
func applyRuntimeUnits(repoPath string, files []string, rootIsRails bool, allFacts []facts.Fact) []facts.Fact {
	present := map[string]bool{}
	roots := map[string]bool{}
	if rootIsRails {
		roots[""] = true
	}
	for _, f := range files {
		present[f] = true
		if f == "config/application.rb" {
			roots[""] = true
		} else if strings.HasSuffix(f, "/config/application.rb") {
			roots[strings.TrimSuffix(f, "/config/application.rb")] = true
		}
	}
	if len(roots) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(roots))
	for r := range roots {
		ordered = append(ordered, r)
	}
	sort.Strings(ordered)

	misses := map[string]int{}
	read := 0
	var out []facts.Fact
	for _, root := range ordered {
		idx := newJobIndex(allFacts, root, ordered)
		r := &appReader{repoPath: repoPath, root: root, present: present, misses: misses}
		for _, u := range r.read() {
			out = append(out, u.fact(idx))
			read++
		}
	}
	if fact, ok := extcoverage.Fact(repoPath, "ruby:runtime-units", "ruby_runtime_unit", read, misses); ok {
		out = append(out, fact)
	}
	return out
}

// ownerRoot is the deepest app root holding a file, or "-" when none does.
func ownerRoot(file string, roots []string) string {
	owner := "-"
	for _, r := range roots {
		if r == "" {
			if owner == "-" {
				owner = ""
			}
			continue
		}
		if strings.HasPrefix(file, r+"/") && (owner == "-" || len(r) > len(owner)) {
			owner = r
		}
	}
	return owner
}

func (u deployUnit) fact(idx jobIndex) facts.Fact {
	props := map[string]any{"language": "ruby"}
	for k, v := range u.props {
		props[k] = v
	}
	var targets []string
	if u.class != "" {
		if idx.classes[u.class] {
			targets = []string{u.class}
		} else {
			props["entrypoint_missing"] = true
		}
	}
	if len(u.queues) > 0 {
		props["queues"] = strings.Join(u.queues, " ")
		if _, ok := props["entrypoint_kind"]; !ok {
			props["entrypoint_kind"] = entryQueues
		}
		targets = append(targets, idx.drainedBy(u.queues)...)
	}
	var rels []facts.Relation
	for _, t := range targets {
		rels = append(rels, facts.Relation{Kind: facts.RelRuns, Target: t})
	}
	return facts.Fact{
		Kind:      facts.KindRuntimeUnit,
		Name:      u.file + "::" + u.name,
		File:      u.file,
		Line:      u.line,
		Props:     props,
		Relations: rels,
	}
}

// appReader reads one Rails app root's deploy files.
type appReader struct {
	repoPath string
	root     string
	present  map[string]bool
	misses   map[string]int
	consumed map[string]bool
}

func (r *appReader) rel(p string) string {
	if r.root == "" {
		return p
	}
	return r.root + "/" + p
}

func (r *appReader) source(rel string) (string, bool) {
	if !r.present[rel] {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(r.repoPath, filepath.FromSlash(rel)))
	if err != nil {
		r.misses["unreadable"]++
		return "", false
	}
	return string(b), true
}

func (r *appReader) read() []deployUnit {
	r.consumed = map[string]bool{}
	var units []deployUnit
	units = append(units, r.procfiles()...)
	units = append(units, r.sidekiqConfigs()...)
	units = append(units, r.periodicSchedule()...)
	units = append(units, r.solidQueue()...)
	units = append(units, r.whenever()...)
	units = append(units, r.kamal()...)
	units = append(units, r.railway()...)
	units = append(units, r.dockerfile()...)
	units = append(units, r.fly()...)
	return units
}

// procfiles reads Procfile (production) and Procfile.<env> beside it.
func (r *appReader) procfiles() []deployUnit {
	var names []string
	prefix := r.rel("Procfile")
	for f := range r.present {
		if f == prefix || (strings.HasPrefix(f, prefix+".") && path.Dir(f) == path.Dir(prefix)) {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	var units []deployUnit
	for _, file := range names {
		src, ok := r.source(file)
		if !ok {
			continue
		}
		env := "production"
		if ext := strings.TrimPrefix(path.Base(file), "Procfile"); ext != "" {
			env = strings.TrimPrefix(ext, ".")
		}
		for i, line := range strings.Split(src, "\n") {
			m := reProcfileLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			u := deployUnit{file: file, name: m[1], line: i + 1, props: map[string]any{
				"unit_kind": unitProcess, "provider": "procfile", "command": m[2], "environment": env,
			}}
			r.classifyCommand(&u, m[2])
			units = append(units, u)
		}
	}
	return units
}

// classifyCommand reads what a command starts: the web server, a Sidekiq worker
// with its config, the Solid Queue supervisor, a queue named in its environment,
// or a script the repository holds.
func (r *appReader) classifyCommand(u *deployUnit, command string) {
	if m := reQueueEnv.FindStringSubmatch(command); m != nil {
		u.queues = splitQueueList(m[1])
	}
	switch {
	case reSidekiq.MatchString(command) || strings.Contains(command, "start-sidekiq"):
		config := r.rel("config/sidekiq.yml")
		if m := reConfigFlag.FindStringSubmatch(command); m != nil {
			config = r.rel(strings.TrimPrefix(m[1], "./"))
		}
		u.props["config"] = config
		if w, ok := r.sidekiqConfig(config); ok {
			r.consumed[config] = true
			if len(u.queues) == 0 {
				u.queues = w.queues
			}
			for k, v := range w.props {
				u.props[k] = v
			}
		}
	case reSolidQueue.MatchString(command):
		if len(u.queues) == 0 {
			u.queues = r.solidQueueQueues()
		}
	case reWebCommand.MatchString(command):
		u.props["entrypoint_kind"] = entryWeb
		return
	}
	if len(u.queues) > 0 {
		return
	}
	if script := r.scriptOf(command); script != "" {
		u.props["entrypoint_kind"] = entryScript
		u.props["entrypoint"] = script
		return
	}
	u.props["entrypoint_kind"] = entryCommand
}

// scriptOf is the first program a command runs, when the repository holds it.
func (r *appReader) scriptOf(command string) string {
	fields := strings.Fields(command)
	for len(fields) > 0 && (fields[0] == "env" || reEnvAssign.MatchString(fields[0])) {
		fields = fields[1:]
	}
	if len(fields) >= 2 && fields[0] == "bundle" && fields[1] == "exec" {
		fields = fields[2:]
	}
	if len(fields) == 0 {
		return ""
	}
	p := strings.Trim(strings.TrimPrefix(fields[0], "./"), `"'[],`)
	if r.present[r.rel(p)] {
		return r.rel(p)
	}
	return ""
}

func splitQueueList(v string) []string {
	var out []string
	for _, q := range strings.Split(strings.Trim(v, `"'`), ",") {
		if q = strings.TrimSpace(q); q != "" {
			out = append(out, q)
		}
	}
	return out
}

// sidekiqReading is what a Sidekiq config states: its queues, in order, and
// its concurrency where it is a literal.
type sidekiqReading struct {
	queues []string
	props  map[string]any
}

func (r *appReader) sidekiqConfig(rel string) (sidekiqReading, bool) {
	src, ok := r.source(rel)
	if !ok {
		return sidekiqReading{}, false
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(withoutERB(src)), &doc); err != nil {
		r.misses["unparsable_yaml"]++
		return sidekiqReading{}, false
	}
	w := sidekiqReading{props: map[string]any{}}
	for _, item := range listOf(keyed(doc, "queues")) {
		switch q := item.(type) {
		case string:
			w.queues = append(w.queues, q)
		case []any:
			if len(q) > 0 {
				if s, ok := q[0].(string); ok {
					w.queues = append(w.queues, s)
				}
			}
		}
	}
	setLiteral(w.props, "concurrency", keyed(doc, "concurrency"))
	return w, true
}

// sidekiqConfigs reads config/sidekiq*.yml files no process named.
func (r *appReader) sidekiqConfigs() []deployUnit {
	var names []string
	dir := r.rel("config") + "/"
	for f := range r.present {
		base := path.Base(f)
		if strings.HasPrefix(f, dir) && path.Dir(f)+"/" == dir && strings.HasPrefix(base, "sidekiq") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")) {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	var units []deployUnit
	for _, file := range names {
		if r.consumed[file] {
			continue
		}
		w, ok := r.sidekiqConfig(file)
		if !ok {
			continue
		}
		props := map[string]any{"unit_kind": unitWorker, "provider": "sidekiq"}
		for k, v := range w.props {
			props[k] = v
		}
		units = append(units, deployUnit{file: file, name: "sidekiq", line: 1, props: props, queues: w.queues})
	}
	return units
}

// periodicSchedule reads config/schedule.yml, the shape Sidekiq Enterprise's
// periodic jobs, sidekiq-cron and sidekiq-scheduler share: a name, a cron and a class.
func (r *appReader) periodicSchedule() []deployUnit {
	file := r.rel("config/schedule.yml")
	src, ok := r.source(file)
	if !ok {
		return nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(withoutERB(src)), &root); err != nil || len(root.Content) == 0 {
		r.misses["unparsable_yaml"]++
		return nil
	}
	var units []deployUnit
	forEachPair(root.Content[0], func(key string, val *yaml.Node, line int) {
		var entry map[string]any
		if val.Decode(&entry) != nil {
			return
		}
		class := stringOf(keyed(entry, "class"))
		schedule := stringOf(keyed(entry, "cron"))
		if schedule == "" {
			schedule = stringOf(keyed(entry, "every"))
		}
		if class == "" || schedule == "" {
			return
		}
		props := map[string]any{
			"unit_kind": unitSchedule, "provider": "sidekiq-periodic", "schedule": schedule,
			"entrypoint": class, "entrypoint_kind": entryJobClass,
		}
		if q := stringOf(keyed(entry, "queue")); q != "" {
			props["queue"] = q
		}
		units = append(units, deployUnit{file: file, name: key, line: line, props: props, class: class})
	})
	return units
}

// solidQueueVersion is the locked solid_queue's major and minor, or false.
func (r *appReader) solidQueueVersion() (int, int, bool) {
	src, ok := r.source(r.rel("Gemfile.lock"))
	if !ok {
		return 0, 0, false
	}
	m := reSolidQueueV.FindStringSubmatch(src)
	if m == nil {
		return 0, 0, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major, minor, true
}

// solidQueueEnv is the environment a config is read for: production where the
// file declares it, else default.
func solidQueueEnv(doc map[string]any) string {
	if _, ok := doc["production"]; ok {
		return "production"
	}
	return "default"
}

func (r *appReader) solidQueueDoc() (map[string]any, *yaml.Node, string, bool) {
	file := r.rel("config/queue.yml")
	src, ok := r.source(file)
	if !ok {
		return nil, nil, file, false
	}
	var doc map[string]any
	var root yaml.Node
	clean := []byte(withoutERB(src))
	if yaml.Unmarshal(clean, &doc) != nil || yaml.Unmarshal(clean, &root) != nil || len(root.Content) == 0 {
		r.misses["unparsable_yaml"]++
		return nil, nil, file, false
	}
	return doc, root.Content[0], file, true
}

// solidQueueQueues is every queue the Solid Queue workers drain, for a process
// that starts the supervisor.
func (r *appReader) solidQueueQueues() []string {
	doc, _, _, ok := r.solidQueueDoc()
	if !ok {
		return nil
	}
	env, _ := doc[solidQueueEnv(doc)].(map[string]any)
	seen := map[string]bool{}
	var out []string
	for _, w := range listOf(env["workers"]) {
		wm, _ := w.(map[string]any)
		for _, q := range queuesOf(wm["queues"]) {
			if !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
		}
	}
	return out
}

func queuesOf(v any) []string {
	switch q := v.(type) {
	case string:
		return splitQueueList(q)
	case []any:
		var out []string
		for _, item := range q {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// solidQueue reads config/queue.yml's workers and config/recurring.yml's tasks,
// and a recurring: key in queue.yml, which Solid Queue stopped reading in 0.9.
func (r *appReader) solidQueue() []deployUnit {
	var units []deployUnit
	if doc, root, file, ok := r.solidQueueDoc(); ok {
		env := solidQueueEnv(doc)
		section, _ := doc[env].(map[string]any)
		envNode := valueNode(root, env)
		workersNode := valueNode(envNode, "workers")
		for i, w := range listOf(section["workers"]) {
			wm, _ := w.(map[string]any)
			line := 1
			if workersNode != nil && i < len(workersNode.Content) {
				line = workersNode.Content[i].Line
			} else if envNode != nil {
				line = envNode.Line
			}
			props := map[string]any{"unit_kind": unitWorker, "provider": "solid_queue", "environment": env}
			setLiteral(props, "threads", wm["threads"])
			setLiteral(props, "processes", wm["processes"])
			units = append(units, deployUnit{
				file: file, name: env + "/workers/" + strconv.Itoa(i), line: line, props: props,
				queues: queuesOf(wm["queues"]),
			})
		}
		notRead := ""
		if major, minor, known := r.solidQueueVersion(); known && (major > 0 || minor >= 9) {
			notRead = "solid_queue " + strconv.Itoa(major) + "." + strconv.Itoa(minor) + " reads recurring tasks from config/recurring.yml since 0.9, not from queue.yml"
		}
		forEachPair(root, func(envName string, envVal *yaml.Node, _ int) {
			recurring := literalValueNode(envVal, "recurring")
			if recurring == nil {
				return
			}
			for _, u := range recurringUnits(file, envName+"/recurring/", recurring) {
				if notRead != "" {
					u.props["read_by_runtime"] = false
					u.props["not_read_because"] = notRead
				}
				units = append(units, u)
			}
		})
	}

	file := r.rel("config/recurring.yml")
	if src, ok := r.source(file); ok {
		var doc map[string]any
		var root yaml.Node
		clean := []byte(withoutERB(src))
		if yaml.Unmarshal(clean, &doc) != nil || yaml.Unmarshal(clean, &root) != nil || len(root.Content) == 0 {
			r.misses["unparsable_yaml"]++
		} else {
			env := solidQueueEnv(doc)
			if node := valueNode(root.Content[0], env); node != nil {
				for _, u := range recurringUnits(file, env+"/", node) {
					u.props["read_by_runtime"] = true
					units = append(units, u)
				}
			}
		}
	}
	return units
}

func recurringUnits(file, prefix string, node *yaml.Node) []deployUnit {
	var units []deployUnit
	forEachPair(node, func(key string, val *yaml.Node, line int) {
		var entry map[string]any
		if val.Decode(&entry) != nil {
			return
		}
		props := map[string]any{"unit_kind": unitSchedule, "provider": "solid_queue"}
		if s := stringOf(entry["schedule"]); s != "" {
			props["schedule"] = s
		}
		u := deployUnit{file: file, name: prefix + key, line: line, props: props}
		if class := stringOf(entry["class"]); class != "" {
			props["entrypoint"] = class
			props["entrypoint_kind"] = entryJobClass
			u.class = class
		} else if cmd := stringOf(entry["command"]); cmd != "" {
			props["command"] = cmd
			props["entrypoint_kind"] = entryCommand
		}
		if q := stringOf(entry["queue"]); q != "" {
			props["queue"] = q
		}
		units = append(units, u)
	})
	return units
}

// whenever reads config/schedule.rb: each runner, rake or command line inside
// an every block is a unit named by its line.
func (r *appReader) whenever() []deployUnit {
	file := r.rel("config/schedule.rb")
	src, ok := r.source(file)
	if !ok {
		return nil
	}
	var units []deployUnit
	schedule := ""
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if m := reWheneverEach.FindStringSubmatch(line); m != nil {
			schedule = m[1]
			continue
		}
		m := reWheneverJob.FindStringSubmatch(line)
		if m == nil || schedule == "" {
			continue
		}
		props := map[string]any{"unit_kind": unitSchedule, "provider": "whenever", "schedule": schedule}
		u := deployUnit{file: file, name: strconv.Itoa(i + 1), line: i + 1, props: props}
		switch m[1] {
		case "runner":
			props["entrypoint_kind"] = entryRunner
			props["command"] = m[2]
			if c := reLeadingConst.FindStringSubmatch(m[2]); c != nil {
				props["entrypoint"] = c[1]
				u.class = c[1]
			}
		case "rake":
			props["entrypoint_kind"] = entryRake
			props["entrypoint"] = m[2]
		default:
			props["entrypoint_kind"] = entryCommand
			props["command"] = m[2]
		}
		units = append(units, u)
	}
	return units
}

// kamal reads config/deploy.yml's roles and accessories.
func (r *appReader) kamal() []deployUnit {
	file := r.rel("config/deploy.yml")
	src, ok := r.source(file)
	if !ok {
		return nil
	}
	var root yaml.Node
	if yaml.Unmarshal([]byte(withoutERB(src)), &root) != nil || len(root.Content) == 0 {
		r.misses["unparsable_yaml"]++
		return nil
	}
	doc := root.Content[0]
	var units []deployUnit
	servers := valueNode(doc, "servers")
	switch {
	case servers == nil:
	case servers.Kind == yaml.SequenceNode:
		units = append(units, r.kamalRole(file, "web", servers, servers.Line))
	case servers.Kind == yaml.MappingNode:
		forEachPair(servers, func(role string, val *yaml.Node, line int) {
			units = append(units, r.kamalRole(file, role, val, line))
		})
	}
	if acc := valueNode(doc, "accessories"); acc != nil {
		forEachPair(acc, func(name string, val *yaml.Node, line int) {
			var spec map[string]any
			if val.Decode(&spec) != nil {
				return
			}
			props := map[string]any{"unit_kind": unitAccessory, "provider": "kamal"}
			if img := stringOf(spec["image"]); img != "" {
				props["image"] = img
			}
			hosts := hostsOf(spec["host"])
			hosts = append(hosts, hostsOf(spec["hosts"])...)
			setHosts(props, hosts)
			if cmd := stringOf(spec["cmd"]); cmd != "" {
				props["command"] = cmd
			}
			units = append(units, deployUnit{file: file, name: "accessory/" + name, line: line, props: props})
		})
	}
	return units
}

func (r *appReader) kamalRole(file, role string, val *yaml.Node, line int) deployUnit {
	props := map[string]any{"unit_kind": unitRole, "provider": "kamal"}
	u := deployUnit{file: file, name: role, line: line, props: props}
	var hosts []string
	cmd := ""
	switch val.Kind {
	case yaml.SequenceNode:
		var list []any
		_ = val.Decode(&list)
		hosts = hostsOf(list)
	case yaml.MappingNode:
		var spec map[string]any
		_ = val.Decode(&spec)
		hosts = hostsOf(spec["hosts"])
		cmd = stringOf(spec["cmd"])
	}
	setHosts(props, hosts)
	if cmd == "" {
		if role == "web" {
			props["entrypoint_kind"] = entryWeb
		}
		return u
	}
	props["command"] = cmd
	r.classifyCommand(&u, cmd)
	return u
}

func hostsOf(v any) []string {
	switch h := v.(type) {
	case string:
		return []string{h}
	case []any:
		var out []string
		for _, item := range h {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func setHosts(props map[string]any, hosts []string) {
	if len(hosts) == 0 {
		return
	}
	props["hosts"] = strings.Join(hosts, " ")
	for _, h := range hosts {
		if h == kamalPlaceholderHost {
			props["placeholder_host"] = true
		}
	}
}

// railway reads railway.toml's start command and replica count.
func (r *appReader) railway() []deployUnit {
	file := r.rel("railway.toml")
	src, ok := r.source(file)
	if !ok {
		return nil
	}
	tables := readTOML(src)
	deploy, ok := tables["deploy"]
	if !ok || deploy.values["startCommand"] == "" {
		return nil
	}
	props := map[string]any{"unit_kind": unitProcess, "provider": "railway"}
	u := deployUnit{file: file, name: "deploy", line: deploy.lines["startCommand"], props: props}
	props["command"] = deploy.values["startCommand"]
	if n := deploy.values["numReplicas"]; n != "" {
		props["replicas"] = n
	}
	r.classifyCommand(&u, deploy.values["startCommand"])
	return []deployUnit{u}
}

// fly reads fly.toml's [processes].
func (r *appReader) fly() []deployUnit {
	file := r.rel("fly.toml")
	src, ok := r.source(file)
	if !ok {
		return nil
	}
	procs, ok := readTOML(src)["processes"]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(procs.values))
	for name := range procs.values {
		names = append(names, name)
	}
	sort.Strings(names)
	var units []deployUnit
	for _, name := range names {
		props := map[string]any{"unit_kind": unitProcess, "provider": "fly", "command": procs.values[name]}
		u := deployUnit{file: file, name: name, line: procs.lines[name], props: props}
		r.classifyCommand(&u, procs.values[name])
		units = append(units, u)
	}
	return units
}

// dockerfile reads the image's last CMD, the command a container runs.
func (r *appReader) dockerfile() []deployUnit {
	file := r.rel("Dockerfile")
	src, ok := r.source(file)
	if !ok {
		return nil
	}
	cmd, at := "", 0
	for i, line := range strings.Split(src, "\n") {
		if m := reDockerCMD.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			cmd, at = dockerCommand(m[1]), i+1
		}
	}
	if cmd == "" {
		return nil
	}
	props := map[string]any{"unit_kind": unitContainer, "provider": "docker", "command": cmd}
	u := deployUnit{file: file, name: "cmd", line: at, props: props}
	r.classifyCommand(&u, cmd)
	return []deployUnit{u}
}

// dockerCommand renders an exec-form CMD as the words it runs.
func dockerCommand(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") {
		return v
	}
	var words []string
	if yaml.Unmarshal([]byte(v), &words) != nil {
		return v
	}
	return strings.Join(words, " ")
}

type tomlTable struct {
	values map[string]string
	lines  map[string]int
}

// readTOML reads the flat key = value pairs of each table, the part of a deploy
// tool's TOML that names a command; nested and multi-line values are not read.
func readTOML(src string) map[string]tomlTable {
	tables := map[string]tomlTable{}
	current := ""
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reTOMLTable.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		m := reTOMLPair.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, ok := tables[current]
		if !ok {
			t = tomlTable{values: map[string]string{}, lines: map[string]int{}}
			tables[current] = t
		}
		t.values[m[1]] = strings.Trim(m[2], `"'`)
		t.lines[m[1]] = i + 1
	}
	return tables
}

// withoutERB stands a literal in for each ERB tag, keeping the line count so a
// YAML node's line is still the file's.
func withoutERB(src string) string {
	return reERB.ReplaceAllStringFunc(src, func(tag string) string {
		return `"` + erbToken + `"` + strings.Repeat("\n", strings.Count(tag, "\n"))
	})
}

// keyed reads a Sidekiq-style key written either as `queues:` or `:queues:`.
func keyed(m map[string]any, key string) any {
	if v, ok := m[key]; ok {
		return v
	}
	return m[":"+key]
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func stringOf(v any) string {
	switch s := v.(type) {
	case string:
		if s == erbToken {
			return ""
		}
		return s
	case int:
		return strconv.Itoa(s)
	}
	return ""
}

// setLiteral records a numeric setting as written, or marks it unresolved when
// the file computes it.
func setLiteral(props map[string]any, key string, v any) {
	switch x := v.(type) {
	case nil:
	case string:
		if x == erbToken {
			props[key+"_unresolved"] = true
			return
		}
		props[key] = x
	case int:
		props[key] = strconv.Itoa(x)
	case float64:
		props[key] = strconv.FormatFloat(x, 'f', -1, 64)
	}
}

// forEachPair walks a mapping node's literal keys in order, a merge key aside.
func forEachPair(n *yaml.Node, fn func(key string, val *yaml.Node, line int)) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Value == "<<" {
			continue
		}
		fn(k.Value, n.Content[i+1], k.Line)
	}
}

// valueNode is a key's value in a mapping, following a merge key's alias.
func valueNode(n *yaml.Node, key string) *yaml.Node {
	if v := literalValueNode(n, key); v != nil {
		return v
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "<<" {
			target := n.Content[i+1]
			if target.Kind == yaml.AliasNode {
				target = target.Alias
			}
			if v := valueNode(target, key); v != nil {
				return v
			}
		}
	}
	return nil
}

// literalValueNode is a key's value only where the mapping itself writes it.
func literalValueNode(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

package rubyextractor

import (
	"sort"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// A Rails app at the root, its deploy files and the job classes they run.
func runtimeRepo(t *testing.T, files map[string]string) []facts.Fact {
	t.Helper()
	all := map[string]string{
		"config/application.rb": "module Shop\n  class Application < Rails::Application\n  end\nend\n",
	}
	for k, v := range files {
		all[k] = v
	}
	return extractRepo(t, all)
}

func runtimeUnit(t *testing.T, ff []facts.Fact, name string) facts.Fact {
	t.Helper()
	var names []string
	for _, f := range ff {
		if f.Kind == facts.KindRuntimeUnit {
			if f.Name == name {
				return f
			}
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	t.Fatalf("no runtime unit %s among %v", name, names)
	return facts.Fact{}
}

func runtimeUnits(ff []facts.Fact) []facts.Fact {
	var out []facts.Fact
	for _, f := range ff {
		if f.Kind == facts.KindRuntimeUnit {
			out = append(out, f)
		}
	}
	return out
}

func runsTargets(f facts.Fact) []string {
	var out []string
	for _, r := range f.Relations {
		if r.Kind == facts.RelRuns {
			out = append(out, r.Target)
		}
	}
	sort.Strings(out)
	return out
}

const shopJobs = `class ReportJob < ApplicationJob
  queue_as :reports
end
`

const shopMailJob = `class MailJob < ApplicationJob
  queue_as :mailers
end
`

const shopPlainJob = `class TidyJob < ApplicationJob
  def perform; end
end
`

func TestRuntimeUnits_AProcfileWorkerRunsTheJobsOfTheQueuesItsConfigLists(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"Procfile": "web: bundle exec puma -C config/puma.rb\nworker: env MALLOC_ARENA_MAX=2 bin/start-sidekiq\nslow: bin/start-sidekiq -C config/sidekiq-slow.yml\n",
		"config/sidekiq.yml": `---
:concurrency: <%= ENV['SIDEKIQ_CONCURRENCY'] || 5 %>
:queues:
  - ['default', <%= ENV.fetch('PRIORITY_DEFAULT', 8)%>]
  - [
      'mailers',
      <%= ENV.fetch('PRIORITY_MAILERS',
      7)%>,
    ]
`,
		"config/sidekiq-slow.yml": ":concurrency: 2\n:queues:\n  - reports\n",
		"app/jobs/report_job.rb":  shopJobs,
		"app/jobs/mail_job.rb":    shopMailJob,
		"app/jobs/tidy_job.rb":    shopPlainJob,
	})

	web := runtimeUnit(t, ff, "Procfile::web")
	if web.PropString("unit_kind") != "process" || web.PropString("provider") != "procfile" {
		t.Fatalf("web: %+v", web.Props)
	}
	if web.PropString("entrypoint_kind") != "web" {
		t.Fatalf("a puma process serves the web: %+v", web.Props)
	}
	if web.PropString("command") != "bundle exec puma -C config/puma.rb" {
		t.Fatalf("command = %q", web.PropString("command"))
	}

	worker := runtimeUnit(t, ff, "Procfile::worker")
	if got := worker.PropString("queues"); got != "default mailers" {
		t.Fatalf("worker queues = %q, from config/sidekiq.yml when no -C is given", got)
	}
	if got := worker.PropString("config"); got != "config/sidekiq.yml" {
		t.Fatalf("worker config = %q", got)
	}
	if !worker.PropBool("concurrency_unresolved") {
		t.Fatalf("an ERB concurrency is unresolved, never guessed: %+v", worker.Props)
	}
	// TidyJob names no queue, so it is on the default queue.
	if got := strings.Join(runsTargets(worker), ","); got != "MailJob,TidyJob" {
		t.Fatalf("worker runs %s", got)
	}

	slow := runtimeUnit(t, ff, "Procfile::slow")
	if slow.PropString("queues") != "reports" || slow.PropString("concurrency") != "2" {
		t.Fatalf("slow: %+v", slow.Props)
	}
	if got := strings.Join(runsTargets(slow), ","); got != "ReportJob" {
		t.Fatalf("slow runs %s", got)
	}

	for _, u := range runtimeUnits(ff) {
		if strings.HasPrefix(u.Name, "config/sidekiq") {
			t.Fatalf("a Sidekiq config a process names is read into that process, not a unit of its own: %s", u.Name)
		}
	}
}

func TestRuntimeUnits_AQueueNamedInTheCommandsEnvironmentIsTheWorkersQueue(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"Procfile":               "async: env ASYNC_ADAPTER_ACTIVE_JOB_QUEUE_NAMES=reports bin/async-job-server\n",
		"app/jobs/report_job.rb": shopJobs,
	})
	u := runtimeUnit(t, ff, "Procfile::async")
	if u.PropString("queues") != "reports" {
		t.Fatalf("queues = %q", u.PropString("queues"))
	}
	if got := strings.Join(runsTargets(u), ","); got != "ReportJob" {
		t.Fatalf("runs %s", got)
	}
}

func TestRuntimeUnits_APeriodicScheduleRunsItsClassAndNamesOneThatDoesNotExist(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"config/schedule.yml": `nightly_report:
  cron: '0 3 * * *'
  class: 'ReportJob'

gone:
  cron: '0 * * * *'
  class: 'Reports::GoneJob'
`,
		"app/jobs/report_job.rb": shopJobs,
	})
	u := runtimeUnit(t, ff, "config/schedule.yml::nightly_report")
	if u.PropString("unit_kind") != "schedule" || u.PropString("schedule") != "0 3 * * *" {
		t.Fatalf("nightly_report: %+v", u.Props)
	}
	if u.PropString("entrypoint") != "ReportJob" || u.PropString("entrypoint_kind") != "job_class" {
		t.Fatalf("entrypoint: %+v", u.Props)
	}
	if got := strings.Join(runsTargets(u), ","); got != "ReportJob" {
		t.Fatalf("runs %s", got)
	}
	if u.PropBool("entrypoint_missing") {
		t.Fatalf("ReportJob exists: %+v", u.Props)
	}
	gone := runtimeUnit(t, ff, "config/schedule.yml::gone")
	if !gone.PropBool("entrypoint_missing") || len(runsTargets(gone)) != 0 {
		t.Fatalf("a schedule naming no class that exists is said so and runs nothing: %+v %v", gone.Props, gone.Relations)
	}
}

func TestRuntimeUnits_SolidQueueWorkersRecurringTasksAndAKeyTheLockedVersionDoesNotRead(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"Gemfile.lock": "GEM\n  specs:\n    solid_queue (1.2.4)\n",
		"config/queue.yml": `default: &default
  recurring:
    stats:
      class: ReportJob
      schedule: every 5 minutes
  workers:
    - queues: [reports]
      threads: 1
      processes: 1
    - queues: "*"
      threads: 3
      processes: <%= ENV.fetch("JOB_CONCURRENCY", 1) %>

development:
  <<: *default

production:
  workers:
    - queues: [reports]
      threads: 1
      processes: 1
    - queues: "*"
      threads: 5
      processes: 2
`,
		"config/recurring.yml": `production:
  nightly:
    class: MailJob
    schedule: every day at 3am
  sweep:
    command: "Thing.sweep"
    schedule: every hour
`,
		"app/jobs/report_job.rb": shopJobs,
		"app/jobs/mail_job.rb":   shopMailJob,
	})

	all := runtimeUnit(t, ff, "config/queue.yml::production/workers/1")
	if all.PropString("unit_kind") != "worker" || all.PropString("provider") != "solid_queue" {
		t.Fatalf("worker: %+v", all.Props)
	}
	if all.PropString("queues") != "*" || all.PropString("threads") != "5" || all.PropString("environment") != "production" {
		t.Fatalf("worker props: %+v", all.Props)
	}
	if got := strings.Join(runsTargets(all), ","); got != "MailJob,ReportJob" {
		t.Fatalf("a worker on every queue runs every job class: %s", got)
	}
	for _, u := range runtimeUnits(ff) {
		if strings.HasPrefix(u.Name, "config/queue.yml::development/") {
			t.Fatalf("production is read where it is declared, not every environment: %s", u.Name)
		}
	}

	nightly := runtimeUnit(t, ff, "config/recurring.yml::production/nightly")
	if nightly.PropString("schedule") != "every day at 3am" || strings.Join(runsTargets(nightly), ",") != "MailJob" {
		t.Fatalf("nightly: %+v %v", nightly.Props, nightly.Relations)
	}
	if !nightly.PropBool("read_by_runtime") {
		t.Fatalf("recurring.yml is read: %+v", nightly.Props)
	}
	sweep := runtimeUnit(t, ff, "config/recurring.yml::production/sweep")
	if sweep.PropString("entrypoint_kind") != "command" || sweep.PropString("command") != "Thing.sweep" {
		t.Fatalf("sweep: %+v", sweep.Props)
	}

	legacy := runtimeUnit(t, ff, "config/queue.yml::default/recurring/stats")
	if legacy.PropBool("read_by_runtime") {
		t.Fatalf("solid_queue 1.2.4 reads recurring tasks from config/recurring.yml, not queue.yml: %+v", legacy.Props)
	}
	if !strings.Contains(legacy.PropString("not_read_because"), "recurring.yml") {
		t.Fatalf("not_read_because = %q", legacy.PropString("not_read_because"))
	}
}

func TestRuntimeUnits_WheneverRunnerRakeAndCommand(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"config/schedule.rb": `every 1.day, at: '4:30 am' do
  runner "ReportJob.perform_later"
end

every :hour do
  rake "shop:tidy"
  command "/usr/bin/true"
end
`,
		"app/jobs/report_job.rb": shopJobs,
	})
	var whenever []facts.Fact
	for _, u := range runtimeUnits(ff) {
		if u.PropString("provider") == "whenever" {
			whenever = append(whenever, u)
		}
	}
	if len(whenever) != 3 {
		t.Fatalf("three whenever units, got %d", len(whenever))
	}
	runner := runtimeUnit(t, ff, "config/schedule.rb::2")
	if runner.PropString("schedule") != "1.day, at: '4:30 am'" || strings.Join(runsTargets(runner), ",") != "ReportJob" {
		t.Fatalf("runner: %+v %v", runner.Props, runner.Relations)
	}
	rake := runtimeUnit(t, ff, "config/schedule.rb::6")
	if rake.PropString("entrypoint_kind") != "rake_task" || rake.PropString("entrypoint") != "shop:tidy" || rake.PropString("schedule") != ":hour" {
		t.Fatalf("rake: %+v", rake.Props)
	}
	cmd := runtimeUnit(t, ff, "config/schedule.rb::7")
	if cmd.PropString("entrypoint_kind") != "command" {
		t.Fatalf("command: %+v", cmd.Props)
	}
}

func TestRuntimeUnits_KamalRolesAccessoriesAndAPlaceholderHost(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"config/deploy.yml": `service: shop
image: shop
servers:
  web:
    - 192.168.0.1
  job:
    hosts:
      - 192.168.0.1
    cmd: bin/jobs
accessories:
  db:
    image: postgres:16
    host: 10.0.0.5
`,
		"config/queue.yml":       "production:\n  workers:\n    - queues: \"*\"\n      threads: 3\n",
		"bin/jobs":               "#!/usr/bin/env ruby\nrequire_relative \"../config/environment\"\nSolidQueue::Supervisor.start\n",
		"app/jobs/report_job.rb": shopJobs,
	})
	web := runtimeUnit(t, ff, "config/deploy.yml::web")
	if web.PropString("unit_kind") != "role" || web.PropString("provider") != "kamal" || web.PropString("entrypoint_kind") != "web" {
		t.Fatalf("web: %+v", web.Props)
	}
	if !web.PropBool("placeholder_host") || web.PropString("hosts") != "192.168.0.1" {
		t.Fatalf("the generator's placeholder host is said so: %+v", web.Props)
	}
	job := runtimeUnit(t, ff, "config/deploy.yml::job")
	if job.PropString("command") != "bin/jobs" || job.PropString("queues") != "*" {
		t.Fatalf("a role running bin/jobs runs the Solid Queue workers' queues: %+v", job.Props)
	}
	if got := strings.Join(runsTargets(job), ","); got != "ReportJob" {
		t.Fatalf("job runs %s", got)
	}
	db := runtimeUnit(t, ff, "config/deploy.yml::accessory/db")
	if db.PropString("unit_kind") != "accessory" || db.PropString("image") != "postgres:16" || db.PropBool("placeholder_host") {
		t.Fatalf("db: %+v", db.Props)
	}
}

func TestRuntimeUnits_RailwayDockerfileAndFly(t *testing.T) {
	ff := runtimeRepo(t, map[string]string{
		"railway.toml": "[build]\nbuilder = \"DOCKERFILE\"\n\n[deploy]\nstartCommand = \"./bin/rails server\"\nnumReplicas = 1\n",
		"Dockerfile":   "FROM ruby:3.3\nENTRYPOINT [\"/rails/bin/docker-entrypoint\"]\nCMD [\"./bin/thrust\", \"./bin/rails\", \"server\"]\n",
		"fly.toml":     "app = \"shop\"\n\n[processes]\n  app = \"bin/rails server\"\n  worker = \"bin/jobs\"\n",
	})
	railway := runtimeUnit(t, ff, "railway.toml::deploy")
	if railway.PropString("command") != "./bin/rails server" || railway.PropString("entrypoint_kind") != "web" || railway.PropString("replicas") != "1" {
		t.Fatalf("railway: %+v", railway.Props)
	}
	docker := runtimeUnit(t, ff, "Dockerfile::cmd")
	if docker.PropString("unit_kind") != "container" || docker.PropString("command") != "./bin/thrust ./bin/rails server" || docker.PropString("entrypoint_kind") != "web" {
		t.Fatalf("docker: %+v", docker.Props)
	}
	if docker.Line != 3 {
		t.Fatalf("the CMD's line: %d", docker.Line)
	}
	fly := runtimeUnit(t, ff, "fly.toml::worker")
	if fly.PropString("command") != "bin/jobs" || fly.PropString("provider") != "fly" {
		t.Fatalf("fly: %+v", fly.Props)
	}
}

func TestRuntimeUnits_ANestedAppIsReadAtItsOwnRootAndCounted(t *testing.T) {
	ff := extractRepo(t, map[string]string{
		"apps/web/config/application.rb":  "module Shop\n  class Application < Rails::Application\n  end\nend\n",
		"apps/web/Procfile":               "web: bin/rails server\n",
		"apps/web/app/jobs/report_job.rb": shopJobs,
		"tools/Procfile":                  "web: python serve.py\n",
	})
	runtimeUnit(t, ff, "apps/web/Procfile::web")
	for _, u := range runtimeUnits(ff) {
		if strings.HasPrefix(u.Name, "tools/") {
			t.Fatalf("a deploy file under no Rails app is not this extractor's: %s", u.Name)
		}
	}
	var coverage facts.Fact
	for _, f := range ff {
		if f.Kind == facts.KindExtraction && f.Name == "ruby:runtime-units" {
			coverage = f
		}
	}
	if coverage.Name == "" {
		t.Fatalf("the reader reports what it read")
	}
}

func TestRuntimeUnits_AWorkerRunsOnlyItsOwnAppsJobs(t *testing.T) {
	app := "module Shop\n  class Application < Rails::Application\n  end\nend\n"
	ff := extractRepo(t, map[string]string{
		"config/application.rb":             app,
		"config/queue.yml":                  "production:\n  workers:\n    - queues: \"*\"\n",
		"app/jobs/report_job.rb":            shopJobs,
		"vendor/demo/config/application.rb": app,
		"vendor/demo/config/queue.yml":      "production:\n  workers:\n    - queues: [default, mailers]\n",
		"vendor/demo/app/jobs/mail_job.rb":  shopMailJob,
		"vendor/demo/app/jobs/tidy_job.rb":  shopPlainJob,
	})
	root := runtimeUnit(t, ff, "config/queue.yml::production/workers/0")
	if got := strings.Join(runsTargets(root), ","); got != "ReportJob" {
		t.Fatalf("the root app's worker runs %s, want only its own ReportJob", got)
	}
	nested := runtimeUnit(t, ff, "vendor/demo/config/queue.yml::production/workers/0")
	if got := strings.Join(runsTargets(nested), ","); got != "MailJob,TidyJob" {
		t.Fatalf("the nested app's worker runs %s, want only its own MailJob and TidyJob", got)
	}
}

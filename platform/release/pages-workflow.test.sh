#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)

ruby -ryaml - "$root/.github/workflows/pages.yml" <<'RUBY'
workflow = YAML.safe_load(File.read(ARGV.fetch(0)))
triggers = workflow.fetch("on")

# The site's push trigger is limited to main branch docs changes for publication.
abort "wrong Pages workflow events" unless triggers.keys.sort == %w[pull_request push workflow_dispatch]
abort "Pages push must select main only" unless triggers.fetch("push").fetch("branches") == ["main"]
abort "Pages push cannot select tags" if triggers.fetch("push").key?("tags")
abort "Pages push must select the public source paths" unless triggers.fetch("push").fetch("paths").include?("docs/site/**")
abort "Pages must start with read-only repository permissions" unless workflow.fetch("permissions") == {"contents" => "read"}

# Build and deploy use separate permissions and deploy requires protected main.
jobs = workflow.fetch("jobs")
build = jobs.fetch("build")
deploy = jobs.fetch("deploy")
abort "Pages build cannot write" unless build.fetch("permissions", {"contents" => "read"}) == {"contents" => "read"}
steps = build.fetch("steps")

# PRs verify the site and workflow fixtures. Publishing builds skip these checks.
%w[docs/site/tests/site.test.sh platform/release/pages-workflow.test.sh].each do |fixture|
  checks = steps.select { |step| step.fetch("run", "").include?(fixture) }
  abort "Pages #{fixture} must run only on PRs" unless
    checks.length == 1 && checks.first["if"] == "github.event_name == 'pull_request'"
end
commands = steps.map { |step| step.fetch("run", "") }.join("\n")
abort "Pages build must use the guarded renderer" unless commands.include?("docs/site/build.sh")
abort "Pages must depend on the build" unless [deploy.fetch("needs")].flatten == ["build"]
guard = deploy.fetch("if")
abort "Pages deploy requires reviewed main and excludes PRs" unless
  guard.include?("github.ref_protected") && guard.include?("refs/heads/main") &&
  guard.include?("jflatten/ShiftBox") && guard.include?("pull_request")
abort "Pages deploy must acknowledge its isolated environment" unless deploy.fetch("environment").fetch("name") == "github-pages"
abort "Pages deploy granted unrelated permissions" unless deploy.fetch("permissions") == {"pages" => "write", "id-token" => "write"}

# This workflow can deploy one Pages artifact. Release and AWS mutation tooling are absent.
deploy_steps = deploy.fetch("steps")
abort "Pages deploy can only deploy its artifact" unless deploy_steps.length == 1 &&
  deploy_steps.first.fetch("uses").start_with?("actions/deploy-pages@")
abort "Pages workflow contains package, release or AWS write authority" if File.read(ARGV.fetch(0)).match?(
  /packages:\s*write|contents:\s*write|aws-actions\/|gh release|aws ec2|aws s3|docker push/
)
RUBY

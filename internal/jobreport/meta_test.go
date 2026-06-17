package jobreport

import "testing"

func TestExtractMetaApp(t *testing.T) {
	cmd := "/home/gitlab-runner/builds/glrtr-gW/0/firewall/datasunrise/workspace/fw/AppBackendService JOB_ID=126741069 SQLITE_CRYPT=FALSE"
	env := "CI_JOB_NAME=int_lin_aws_oracle_trailing_xml_regular, CI_JOB_NAME_SLUG=int-lin-aws-oracle-trailing-xml-regular, CI_PIPELINE_ID=353802, CI_COMMIT_REF_NAME=36136_20147_oracle_qpe, CI_PROJECT_NAME=datasunrise"
	m := extractMeta(cmd, env)
	if m.Shard != "glrtr-gW/0" {
		t.Errorf("Shard = %q", m.Shard)
	}
	if m.JobID != "126741069" {
		t.Errorf("JobID = %q", m.JobID)
	}
	if m.JobSlug != "int-lin-aws-oracle-trailing-xml-regular" {
		t.Errorf("JobSlug = %q", m.JobSlug)
	}
	if m.Pipeline != "353802" {
		t.Errorf("Pipeline = %q", m.Pipeline)
	}
	want := "glrtr-gW/0, JOB_ID=126741069, int-lin-aws-oracle-trailing-xml-regular"
	if got := m.jobCol(); got != want {
		t.Errorf("jobCol = %q, want %q", got, want)
	}
}

func TestExtractMetaWorker(t *testing.T) {
	cmd := "./AppFirewallCore WITH_BACKEND DoubleRunGuard=1 WORKER_ID=1"
	env := "CI_JOB_ID=126739909, CI_JOB_NAME_SLUG=int-lin-aws-mysql8-trailing-regular"
	m := extractMeta(cmd, env)
	if m.WorkerID != "1" {
		t.Errorf("WorkerID = %q", m.WorkerID)
	}
	if m.JobID != "126739909" { // falls back to CI_JOB_ID from environ
		t.Errorf("JobID = %q", m.JobID)
	}
	want := "JOB_ID=126739909, WORKER_ID=1, int-lin-aws-mysql8-trailing-regular"
	if got := m.jobCol(); got != want {
		t.Errorf("jobCol = %q, want %q", got, want)
	}
}

func TestFirstTok(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"bash -l":                    "bash",
		"/usr/bin/gitlab-runner run": "gitlab-runner",
		"java -Xmx128m":              "java",
	}
	for in, want := range cases {
		if got := firstTok(in); got != want {
			t.Errorf("firstTok(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmptyMetaJobCol(t *testing.T) {
	m := extractMeta("", "")
	if m.jobCol() != "" {
		t.Errorf("empty jobCol = %q, want empty", m.jobCol())
	}
}

package main

import "regexp"

// ProcMeta holds the job metadata extracted from a process's cmdline + environ
// labels (gitlab_process_info).
type ProcMeta struct {
	Cmdline  string
	Shard    string // glrtr-XX/N  (runner shard + concurrency slot, from the builds/ path)
	JobID    string
	WorkerID string
	JobName  string
	JobSlug  string
	Pipeline string
	Ref      string
	Project  string
}

var (
	reShard    = regexp.MustCompile(`builds/([^/]+/[0-9]+)/`)
	reJobIDCmd = regexp.MustCompile(`JOB_ID=([0-9]+)`)
	reJobIDEnv = regexp.MustCompile(`CI_JOB_ID=([0-9]+)`)
	reWorker   = regexp.MustCompile(`WORKER_ID=([0-9]+)`)
	reJobName  = regexp.MustCompile(`CI_JOB_NAME=([^,]+)`)
	reJobSlug  = regexp.MustCompile(`CI_JOB_NAME_SLUG=([^,]+)`)
	rePipeline = regexp.MustCompile(`CI_PIPELINE_ID=([0-9]+)`)
	reRef      = regexp.MustCompile(`CI_COMMIT_REF_NAME=([^,]+)`)
	reProject  = regexp.MustCompile(`CI_PROJECT_NAME=([^,]+)`)
)

// cap1 returns the first capture group of re in s, or "".
func cap1(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// extractMeta parses the cmdline + environ label strings into a ProcMeta.
func extractMeta(cmdline, environ string) ProcMeta {
	jobID := cap1(reJobIDCmd, cmdline)
	if jobID == "" {
		jobID = cap1(reJobIDEnv, environ)
	}
	worker := cap1(reWorker, cmdline)
	if worker == "" {
		worker = cap1(reWorker, environ)
	}
	return ProcMeta{
		Cmdline:  cmdline,
		Shard:    cap1(reShard, cmdline),
		JobID:    jobID,
		WorkerID: worker,
		JobName:  cap1(reJobName, environ),
		JobSlug:  cap1(reJobSlug, environ),
		Pipeline: cap1(rePipeline, environ),
		Ref:      cap1(reRef, environ),
		Project:  cap1(reProject, environ),
	}
}

// jobCol renders the compact one-line "job" descriptor used in the table info column.
func (m ProcMeta) jobCol() string {
	parts := make([]string, 0, 4)
	if m.Shard != "" {
		parts = append(parts, m.Shard)
	}
	if m.JobID != "" {
		parts = append(parts, "JOB_ID="+m.JobID)
	}
	if m.WorkerID != "" {
		parts = append(parts, "WORKER_ID="+m.WorkerID)
	}
	if m.JobSlug != "" {
		parts = append(parts, m.JobSlug)
	} else if m.JobName != "" {
		parts = append(parts, m.JobName)
	}
	return joinComma(parts)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// firstTok returns the basename of the first whitespace-separated token of cmdline.
// Null/empty-safe: "" -> "".
func firstTok(cmdline string) string {
	tok := cmdline
	for i := 0; i < len(tok); i++ {
		if tok[i] == ' ' {
			tok = tok[:i]
			break
		}
	}
	for i := len(tok) - 1; i >= 0; i-- {
		if tok[i] == '/' {
			return tok[i+1:]
		}
	}
	return tok
}

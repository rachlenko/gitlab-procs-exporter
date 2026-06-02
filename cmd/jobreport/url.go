package main

import (
	"fmt"
	"strings"
)

// runURL fetches & parses a job-log URL, prints the parsed-from-log section, then
// runs the Prometheus top-N report scoped to the parsed window/node.
func runURL(promURL, jobURL string, topN int) error {
	raw, err := fetchLog(jobURL)
	if err != nil {
		return err
	}
	j := parseJobLog(raw, jobURL)
	printJobSection(j)

	if j.StartEpoch == 0 || j.EndEpoch == 0 {
		fmt.Println("\nNO Prometheus report: could not determine the job window from the log.")
		return nil
	}

	c := newPromClient(promURL)
	step := stepFor(j.StartEpoch, j.EndEpoch)
	node := "all"
	nodeNote := ""
	fellBack := false
	nodes, err := c.resolveNodeByJobID(j.JobID, j.StartEpoch, j.EndEpoch, step)
	if err != nil {
		return err
	}
	switch {
	case len(nodes) == 1:
		node = nodes[0]
		nodeNote = "resolved from JOB_ID via gitlab_process_info"
	case len(nodes) > 1:
		node = "all"
		nodeNote = "JOB_ID seen on multiple nodes: " + strings.Join(nodes, ", ") + " (showing all)"
	default:
		node = "all"
		fellBack = true
		nodeNote = "JOB_ID not found in gitlab_process_info (this job's node isn't scraped — e.g. k8s executor); falling back to ALL nodes for the window"
	}

	fmt.Println("\n" + sep)
	fmt.Printf(" Prometheus top-%d process report   |   proc=all  node=%s\n", topN, node)
	fmt.Printf(" window: %s -> %s UTC   (%ds)\n", fdate(j.StartEpoch), fdate(j.EndEpoch), j.EndEpoch-j.StartEpoch)
	fmt.Printf(" node scope: %s\n", nodeNote)
	fmt.Println(sep)
	if fellBack {
		fmt.Printf("\n ⚠ FLEET-WIDE FALLBACK: the tables below are the top-%d across ALL scraped nodes\n", topN)
		fmt.Println("   for this window — NOT job " + j.JobID + "'s own processes (its node isn't scraped).")
		fmt.Println("   Rows likely belong to unrelated concurrent jobs; cumulative-counter totals")
		fmt.Println("   (CPU/IO via increase()) are fleet aggregates, not this job's usage.")
	}
	return report(c, "all", node, j.StartEpoch, j.EndEpoch, topN)
}

func printJobSection(j JobInfo) {
	fmt.Println(sep)
	fmt.Println(" Parsed from job.log")
	fmt.Println(sep)
	kv := func(k, v string) {
		if v != "" {
			fmt.Printf("  %-12s %s\n", k+":", v)
		}
	}
	kv("JOB_ID", j.JobID)
	kv("name", firstNonEmpty(j.JobSlug, j.JobName))
	kv("pipeline", j.Pipeline)
	kv("project", j.Project)
	kv("runner", strings.TrimSpace(j.Runner+" "+j.Shard))
	kv("system ID", j.SystemID)
	kv("executor", j.Executor)
	if j.Pod != "" {
		kv("pod", j.Namespace+"/"+j.Pod)
	}
	if j.StartEpoch > 0 {
		fmt.Printf("  %-12s %s -> %s UTC  (%s)\n", "window:",
			fdate(j.StartEpoch), fdate(j.EndEpoch), humanDur(j.Duration()))
	}
	if j.Failed {
		fmt.Printf("  %-12s FAILED  (%s)\n", "result:", j.FailMsg)
	} else if j.EndEpoch > 0 {
		kv("result", "ok / no failure marker")
	}

	if len(j.Phases) > 0 {
		fmt.Println("\n  Phase timings:")
		rows := [][]string{{"phase", "duration", "start UTC"}}
		for _, p := range j.Phases {
			rows = append(rows, []string{p.Name, humanDur(p.End - p.Start), fdate(p.Start)})
		}
		fmt.Print(indent(renderTable(rows, 40), "  "))
	}

	if j.CrashKill || j.CoreCmdline != "" || len(j.KilledNames) > 0 {
		fmt.Println("\n  Crash / kill analysis:")
		if j.CrashKill {
			fmt.Println("    • backend unreachable → force-killed DS processes")
		}
		if len(j.KilledNames) > 0 {
			fmt.Printf("    • killed: %s\n", strings.Join(j.KilledNames, ", "))
		}
		if len(j.KillPIDs) > 0 {
			fmt.Printf("    • kill pids: %s\n", strings.Join(j.KillPIDs, ", "))
		}
		if j.CoreCmdline != "" {
			fmt.Printf("    • core dump from: %s\n", j.CoreCmdline)
		}
		for _, f := range j.CrashFrames {
			fmt.Printf("        %s\n", f)
		}
	}

	if len(j.Artifacts) > 0 {
		fmt.Println("\n  Artifacts:")
		for label, u := range j.Artifacts {
			fmt.Printf("    %-18s %s\n", label, u)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func humanDur(sec int64) string {
	if sec <= 0 {
		return "0s"
	}
	m := sec / 60
	s := sec % 60
	if m == 0 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

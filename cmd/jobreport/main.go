package main

import (
	"os"

	"github.com/rachlenko/gitlab-procs-exporter/internal/jobreport"
)

func main() { os.Exit(jobreport.Main(os.Args[1:])) }

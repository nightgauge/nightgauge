// Binary seed is the CLI entry point for the GitLab CE fixture seeder used
// by the integration test harness (#3366). Reads GITLAB_URL and
// GITLAB_ROOT_TOKEN from env, runs the seeder, and emits the Fixtures JSON
// on stdout — the harness pipes that into a tempfile so individual test
// cases can decode it.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/tests/integration/seed"
)

func main() {
	baseURL := os.Getenv("GITLAB_URL")
	if baseURL == "" {
		fmt.Fprintln(os.Stderr, "seed: GITLAB_URL is required")
		os.Exit(2)
	}
	token := os.Getenv("GITLAB_ROOT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "seed: GITLAB_ROOT_TOKEN is required (root-scope PAT)")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s := seed.NewSeeder(baseURL, token)
	fixtures, err := s.Seed(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}

	out, err := seed.FormatJSON(fixtures)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed: marshal fixtures: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

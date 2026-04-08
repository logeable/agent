package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/logeable/agent/internal/agentapp"
	"github.com/logeable/agent/pkg/automation"
)

func main() {
	var (
		profileName string
		provider    string
		model       string
		baseURL     string
		apiKey      string
		jobID       string
		jobName     string
		prompt      string
		sessionKey  string
		interval    time.Duration
		timeout     time.Duration
		maxRetries  int
	)

	flag.StringVar(&profileName, "profile", "agent", "Profile name or path")
	flag.StringVar(&provider, "provider", "", "Provider kind override")
	flag.StringVar(&model, "model", "", "Model override")
	flag.StringVar(&baseURL, "base-url", "", "Base URL override")
	flag.StringVar(&apiKey, "api-key", "", "API key override")
	flag.StringVar(&jobID, "job-id", "agentd:default", "Automation job id")
	flag.StringVar(&jobName, "job-name", "agentd", "Automation job name")
	flag.StringVar(&prompt, "prompt", "", "Prompt to run on each scheduled execution")
	flag.StringVar(&sessionKey, "session", "agentd:default", "Session key for scheduled runs")
	flag.DurationVar(&interval, "interval", 5*time.Minute, "Fixed interval between runs")
	flag.DurationVar(&timeout, "timeout", 2*time.Minute, "Per-run timeout")
	flag.IntVar(&maxRetries, "retries", 0, "Maximum retries per run")
	flag.Parse()

	runtime, err := agentapp.BuildRuntime(agentapp.LoopOptions{
		ProfileName:  profileName,
		ProviderKind: provider,
		ModelName:    model,
		BaseURL:      baseURL,
		APIKey:       apiKey,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer runtime.Close()

	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: -prompt is required")
		os.Exit(1)
	}

	if err := runtime.Automation.Add(automation.JobSpec{
		ID:         jobID,
		Name:       jobName,
		Prompt:     prompt,
		SessionKey: sessionKey,
		Interval:   interval,
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Enabled:    true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := runtime.Automation.Start(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

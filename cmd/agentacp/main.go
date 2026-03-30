package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/logeable/agent/internal/agentacp"
	"github.com/logeable/agent/internal/agentapp"
	"github.com/logeable/agent/pkg/agentcore/agent"
)

func main() {
	var (
		profileName  string
		providerKind string
		modelName    string
		baseURL      string
		apiKey       string
		autoApprove  bool
	)

	flag.StringVar(&profileName, "profile", "agent", "Profile name or path")
	flag.StringVar(&providerKind, "provider", "", "Provider kind to use: openai, openai_response, or ollama")
	flag.StringVar(&modelName, "model", "", "Model name for the selected provider")
	flag.StringVar(&baseURL, "base-url", "", "Base URL for the selected provider")
	flag.StringVar(&apiKey, "api-key", "", "API key for the selected provider")
	flag.BoolVar(&autoApprove, "auto-approve", false, "Automatically approve tool approval requests")
	flag.Parse()

	server := agentacp.NewServer(agentacp.Options{
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		AutoApprove: autoApprove,
		LoopFactory: func(workDir string) (*agent.Loop, error) {
			loop, err := agentapp.BuildLoop(agentapp.LoopOptions{
				ProfileName:  profileName,
				ProviderKind: providerKind,
				ModelName:    modelName,
				BaseURL:      baseURL,
				APIKey:       apiKey,
				WorkDir:      workDir,
			})
			if err != nil {
				return nil, err
			}
			loop.DisableStreaming = false
			return loop, nil
		},
	})

	if err := server.Serve(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

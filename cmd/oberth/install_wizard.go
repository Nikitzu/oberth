package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
)

func engineArgumentPresent(arguments []string) bool {
	for _, argument := range arguments {
		trimmed := strings.TrimSpace(argument)
		if trimmed == "--engine" || trimmed == "-engine" || strings.HasPrefix(trimmed, "--engine=") || strings.HasPrefix(trimmed, "-engine=") {
			return true
		}
		if trimmed == "-h" || trimmed == "--help" || trimmed == "-help" {
			return true
		}
	}
	return false
}

func askInstallChoices(ctx context.Context, input io.Reader, output io.Writer) ([]string, bool, error) {
	reader := bufio.NewReader(input)
	ask := func(question, fallback string) (string, error) {
		fmt.Fprint(output, question)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "" {
			return fallback, nil
		}
		return answer, nil
	}
	fmt.Fprintln(output, "Where should Oberth run?")
	fmt.Fprintln(output, "  [1] On this machine, with Docker      (a laptop; recommended)")
	fmt.Fprintln(output, "  [2] On a Kubernetes cluster           (a shared server; uses your current kubectl context)")
	choice, err := ask("Choice [1]: ", "1")
	if err != nil {
		return nil, false, err
	}
	switch choice {
	case "1", "docker":
	case "2", "kube", "kubernetes":
		fmt.Fprintln(output, "Next time, the same without questions: oberth install --engine=kube")
		fmt.Fprintln(output)
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("%w: answer 1 or 2", errUsage)
	}
	serviceName := "a launchd agent"
	if runtime.GOOS == "linux" {
		serviceName = "a systemd user unit"
	}
	service, err := ask("Keep the server running across logins, as "+serviceName+"? [Y/n]: ", "y")
	if err != nil {
		return nil, false, err
	}
	profile, err := ask("Add the client environment line to your shell profile? [Y/n]: ", "y")
	if err != nil {
		return nil, false, err
	}
	testcontainers, err := ask("Offer Testcontainers to pipelines? (a socket proxy per run; tests can reach your own Docker daemon) [y/N]: ", "n")
	if err != nil {
		return nil, false, err
	}
	arguments := []string{"--engine=docker", "--secretstore"}
	if strings.HasPrefix(service, "y") {
		arguments = append(arguments, "--service")
	}
	if strings.HasPrefix(profile, "y") {
		arguments = append(arguments, "--shell-profile=yes")
	} else {
		arguments = append(arguments, "--shell-profile=no")
	}
	if strings.HasPrefix(testcontainers, "y") {
		arguments = append(arguments, "--testcontainers")
	}
	fmt.Fprintln(output, "Next time, the same without questions: oberth install "+strings.Join(arguments, " "))
	fmt.Fprintln(output)
	return arguments, true, nil
}

func stripEngineKube(arguments []string) []string {
	kept := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		trimmed := strings.TrimSpace(arguments[index])
		if trimmed == "--engine=kube" || trimmed == "-engine=kube" || trimmed == "--engine=argo" || trimmed == "-engine=argo" {
			continue
		}
		if (trimmed == "--engine" || trimmed == "-engine") && index+1 < len(arguments) {
			next := strings.TrimSpace(arguments[index+1])
			if next == "kube" || next == "argo" {
				index++
				continue
			}
		}
		kept = append(kept, arguments[index])
	}
	return kept
}

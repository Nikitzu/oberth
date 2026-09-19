# Getting started

Three commands, then one line per repository. Nothing else to set up.

## 1. Install the CLI

macOS, with Homebrew:

    brew install nikitzu/oberth/oberth

Linux, or a Mac without Homebrew:

    curl -fsSL https://raw.githubusercontent.com/Nikitzu/oberth/poc/install.sh | sh

The script verifies the release checksum and puts `oberth` in `~/.local/bin`.

## 2. Install a server

    oberth install

It asks one question: where should Oberth run.

- **On this machine, with Docker.** The default, and what a laptop wants. It
  needs Docker Desktop or OrbStack running. It sets up a secret store, keeps the
  server running across logins (launchd on macOS, a systemd user unit on Linux)
  and writes the client configuration. Two more yes-or-no questions, both default
  to yes.
- **On a Kubernetes cluster.** For a shared box. It uses your current `kubectl`
  context and continues with the cluster install and its own questions.

At the end it prints the one flag line that repeats the same install without
questions, for scripts and re-runs:

    oberth install --engine=docker --secretstore --service --shell-profile=yes

Add `--testcontainers` when your backend tests start containers
(Testcontainers). Pipelines opt in with the `oberth.ci/testcontainers`
annotation, which `oberth onboard` writes for Maven and Gradle projects that
depend on it; a server installed without the flag refuses such a pipeline at
admission and names the flag.

Open a new shell afterwards (or source `~/.config/oberth/env`), and the
dashboard is at the address the install printed.

## 3. Onboard a repository

From the repository's checkout:

    oberth onboard

It registers the repository, generates a pipeline from the repository's own
workflow and manifests, stores it on the server, adds the `oberth` git remote,
pushes the current commit and waits for the verdict. After that:

    git push oberth HEAD

runs the pipeline for that commit, and `oberth wait <sha>` blocks on the
result. Pushing to GitHub is for green commits.

To take shared steps another repository publishes, for example tzmem's memory
and Notion steps:

    oberth onboard --with transferz/tzmem@steps-v3

## Two servers, one checkout

A laptop server and a team server can both know the same repository. Each
install writes a client profile (`local` for Docker, `server` for a cluster, or
whatever `--name` said). In a checkout:

    oberth use local
    oberth use server

switches where `git push oberth` goes and which server the CLI talks to.

## After a reboot

The secret store comes back sealed. `oberth unseal` unseals it: the Docker
store from your keychain, a cluster store from the key you saved at install.

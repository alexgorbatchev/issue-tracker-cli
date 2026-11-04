package main

import (
	"fmt"
	"os"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/config"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

const (
	skillRepository = "alexgorbatchev/issue-tracker-cli"
	skillBranch     = "main"
)

func arg(name, description string) cobrahelptree.ArgSpec {
	return cobrahelptree.ArgSpec{Name: name, Description: description}
}

var (
	numberArg = arg("<number>", "Ticket number in the tracker repository")
	writeInfo = cobrahelptree.TechInfo{MutatesDB: true}
)

func withArgs(mutates bool, args ...cobrahelptree.ArgSpec) cobrahelptree.TechInfo {
	return cobrahelptree.TechInfo{Args: args, MutatesDB: mutates}
}

var techCatalog = cobrahelptree.TechCatalog{
	"issue-tracker": {
		Env: []cobrahelptree.EnvSpec{
			{Name: config.RepoEnv, Description: "Tracker repository as owner/name; overrides repo in config.toml"},
			{Name: config.StaleDaysEnv, Description: "Days without activity before a Doing ticket is reported as stale; overrides stale_days"},
			{Name: config.RequirePrivateEnv, Description: "Set to true to refuse a tracker that is not private; overrides require_private"},
			{Name: config.AccountEnv, Description: "gh login to use instead of the active one or GH_TOKEN; overrides account"},
			{Name: config.AgentAccountEnv, Description: "gh login to use when AGENT=1, instead of the account setting, except in project add, rm, scan, and import; overrides agent_account"},
		},
	},
	"issue-tracker project add":              withArgs(true, arg("[owner/repo]", "Repository to register; defaults to the current repository")),
	"issue-tracker project inspect":          withArgs(false, arg("[project]", "Project name; defaults to the current repository")),
	"issue-tracker project import":           withArgs(true, arg("<owner/repo>", "Repository whose open issues move into the tracker")),
	"issue-tracker project rm":               withArgs(true, arg("<project>", "Project to unregister")),
	"issue-tracker issue claim":              withArgs(true, arg("[number]", "Ticket to claim; omit when using --next")),
	"issue-tracker issue close":              withArgs(true, numberArg),
	"issue-tracker issue create":             writeInfo,
	"issue-tracker issue edit":               withArgs(true, numberArg),
	"issue-tracker issue inspect":            withArgs(false, arg("<number...>", fmt.Sprintf("Tickets to show, at most %d", tracker.MaxInspect))),
	"issue-tracker issue move":               withArgs(true, numberArg, arg("<project>", "Project that takes over the ticket")),
	"issue-tracker issue release":            withArgs(true, numberArg),
	"issue-tracker issue reopen":             withArgs(true, numberArg),
	"issue-tracker issue attachment add":     withArgs(true, numberArg, arg("<file...>", "Image or video files to upload")),
	"issue-tracker issue attachment pull":    withArgs(false, numberArg),
	"issue-tracker issue blocker add":        withArgs(true, numberArg, arg("<blocker...>", "Tickets that must close first")),
	"issue-tracker issue blocker rm":         withArgs(true, numberArg, arg("<blocker...>", "Blockers to remove")),
	"issue-tracker issue comment add":        withArgs(true, numberArg),
	"issue-tracker issue label add":          withArgs(true, numberArg, arg("<label...>", "Labels to add")),
	"issue-tracker issue label rm":           withArgs(true, numberArg, arg("<label...>", "Labels to remove")),
	"issue-tracker issue status set":         withArgs(true, numberArg, arg("<status>", "Todo or Done")),
	"issue-tracker issue sub add":            withArgs(true, arg("<parent>", "Parent ticket"), arg("<child...>", "Tickets to attach")),
	"issue-tracker issue sub list":           withArgs(false, arg("<parent>", "Parent ticket")),
	"issue-tracker issue sub close-eligible": writeInfo,
	"issue-tracker issue sub rm":             withArgs(true, arg("<parent>", "Parent ticket"), arg("<child...>", "Tickets to detach")),
}

// helpWriteError keeps a failed help write for run, because cobra's help
// function cannot return an error.
var helpWriteError error

func setupHelp(cmd *cobra.Command) {
	err := cobrahelptree.SetupWithOptions(cmd, cobrahelptree.HelpOptions{
		Catalog: techCatalog,
		Skill:   cobrahelptree.SkillSource{Repository: skillRepository, Branch: skillBranch},
		Tree:    cobrahelptree.TreeOptions{HideGeneratedCommands: true},
		OnHelpWriteError: func(_ *cobra.Command, err error) {
			helpWriteError = err
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

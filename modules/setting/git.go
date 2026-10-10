// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gitea.dev/modules/log"
)

// GitHooksSettings are the execution limits for custom server-side Git hook scripts
type GitHooksSettings struct {
	ScriptTimeout int   // max seconds a single hook script may run, 0 = no limit
	TotalTimeout  int   // max cumulative seconds for hook scripts per wrapper invocation, 0 = no limit
	MaxOutputSize int64 // max bytes of a hook script's output forwarded to the pusher, 0 = no limit
}

func (s GitHooksSettings) Validate() error {
	if s.ScriptTimeout < 0 {
		return fmt.Errorf("GitHooksSettings.Validate: [git.hooks] SCRIPT_TIMEOUT must not be negative: %d", s.ScriptTimeout)
	}
	if s.TotalTimeout < 0 {
		return fmt.Errorf("GitHooksSettings.Validate: [git.hooks] TOTAL_TIMEOUT must not be negative: %d", s.TotalTimeout)
	}
	if s.MaxOutputSize < 0 {
		return fmt.Errorf("GitHooksSettings.Validate: [git.hooks] MAX_OUTPUT_SIZE must not be negative: %d", s.MaxOutputSize)
	}
	return nil
}

// Git settings
var Git = struct {
	Path                 string
	HomePath             string
	DisableDiffHighlight bool

	MaxGitDiffLines               int
	MaxGitDiffLineCharacters      int
	MaxGitDiffFiles               int
	CommitsRangeSize              int // CommitsRangeSize the default commits range size
	BranchesRangeSize             int // BranchesRangeSize the default branches range size
	VerbosePush                   bool
	VerbosePushDelay              time.Duration
	GCArgs                        []string `ini:"GC_ARGS" delim:" "`
	EnableAutoGitWireProtocol     bool
	PullRequestPushMessage        bool
	DisableCoreProtectNTFS        bool
	DisablePartialClone           bool
	DiffRenameSimilarityThreshold string
	Timeout                       struct {
		Migrate int
		Mirror  int
		GC      int `ini:"GC"`
	} `ini:"git.timeout"`
	Hooks GitHooksSettings `ini:"-"`
}{
	DisableDiffHighlight:          false,
	MaxGitDiffLines:               1000,
	MaxGitDiffLineCharacters:      5000,
	MaxGitDiffFiles:               100,
	CommitsRangeSize:              50,
	BranchesRangeSize:             20,
	VerbosePush:                   true,
	VerbosePushDelay:              5 * time.Second,
	GCArgs:                        []string{},
	EnableAutoGitWireProtocol:     true,
	PullRequestPushMessage:        true,
	DisablePartialClone:           false,
	DiffRenameSimilarityThreshold: "50%",
	Timeout: struct {
		Migrate int
		Mirror  int
		GC      int `ini:"GC"`
	}{
		Migrate: 600,
		Mirror:  300,
		GC:      60,
	},
}

type GitConfigType struct {
	Options map[string]string // git config key is case-insensitive, always use lower-case
}

func (c *GitConfigType) SetOption(key, val string) {
	c.Options[strings.ToLower(key)] = val
}

func (c *GitConfigType) GetOption(key string) string {
	return c.Options[strings.ToLower(key)]
}

var GitConfig = GitConfigType{
	Options: make(map[string]string),
}

func loadGitFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("git")
	if err := sec.MapTo(&Git); err != nil {
		log.Fatal("Failed to map Git settings: %v", err)
	}

	secGitConfig := rootCfg.Section("git.config")
	GitConfig.Options = make(map[string]string)
	GitConfig.SetOption("diff.algorithm", "histogram")
	GitConfig.SetOption("core.logAllRefUpdates", "true")
	GitConfig.SetOption("gc.reflogExpire", "90")

	secGitReflog := rootCfg.Section("git.reflog")
	if secGitReflog.HasKey("ENABLED") {
		deprecatedSetting(rootCfg, "git.reflog", "ENABLED", "git.config", "core.logAllRefUpdates", "1.21")
		GitConfig.SetOption("core.logAllRefUpdates", secGitReflog.Key("ENABLED").In("true", []string{"true", "false"}))
	}
	if secGitReflog.HasKey("EXPIRATION") {
		deprecatedSetting(rootCfg, "git.reflog", "EXPIRATION", "git.config", "core.reflogExpire", "1.21")
		GitConfig.SetOption("gc.reflogExpire", secGitReflog.Key("EXPIRATION").String())
	}

	for _, key := range secGitConfig.Keys() {
		GitConfig.SetOption(key.Name(), key.String())
	}

	Git.HomePath = sec.Key("HOME_PATH").MustString("home")
	if !filepath.IsAbs(Git.HomePath) {
		Git.HomePath = filepath.Join(AppDataPath, Git.HomePath)
	} else {
		Git.HomePath = filepath.Clean(Git.HomePath)
	}

	// validate for a integer percentage between 0% and 100%
	if !regexp.MustCompile(`^([0-9]|[1-9][0-9]|100)%$`).MatchString(Git.DiffRenameSimilarityThreshold) {
		log.Fatal("Invalid git.DIFF_RENAME_SIMILARITY_THRESHOLD: %s", Git.DiffRenameSimilarityThreshold)
	}

	Git.Hooks = GitHooksSettings{}
	if err := rootCfg.Section("git.hooks").MapTo(&Git.Hooks); err != nil {
		log.Fatal("Failed to map [git.hooks] settings: %v", err)
	}
	if err := Git.Hooks.Validate(); err != nil {
		log.Fatal("%v", err)
	}
}

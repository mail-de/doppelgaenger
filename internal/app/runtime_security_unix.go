//go:build unix

package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/logging"
)

const runtimeSecurityAppliedEnv = "DOPPELGAENGER_RUNTIME_SECURITY_APPLIED"

// ApplyRuntimeSecurity applies optional chroot and privilege dropping on unix systems.
func ApplyRuntimeSecurity(cfg config.Config, logger *slog.Logger) error {
	if !runtimeSecurityRequested(cfg) {
		return nil
	}

	if os.Getenv(runtimeSecurityAppliedEnv) == "1" {
		logger.Log(context.Background(), logging.LevelNotice, "runtime security already applied, skipping re-application")

		return nil
	}

	uid, gid, groups, err := resolveIdentities(cfg)
	if err != nil {
		return err
	}

	if cfg.ChrootDir != "" {
		if err := applyChroot(cfg.ChrootDir); err != nil {
			return err
		}

		logger.Log(context.Background(), logging.LevelNotice, "chroot applied", "path", cfg.ChrootDir)
	}

	if err := applyResolvedIdentities(uid, gid, groups); err != nil {
		return err
	}

	if err := os.Setenv(runtimeSecurityAppliedEnv, "1"); err != nil {
		return fmt.Errorf("set runtime security marker env: %w", err)
	}

	logger.Log(context.Background(), logging.LevelNotice, "runtime security applied", "run_as_user", cfg.RunAsUser, "run_as_group", cfg.RunAsGroup, "supplementary_groups_count", len(groups), "chroot", cfg.ChrootDir != "")

	return nil
}

func runtimeSecurityRequested(cfg config.Config) bool {
	return cfg.RunAsUser != "" || cfg.RunAsGroup != "" || cfg.ChrootDir != ""
}

func applyResolvedIdentities(uid, gid *int, groups []int) error {
	if len(groups) > 0 {
		if err := unix.Setgroups(groups); err != nil {
			return fmt.Errorf("set supplementary groups: %w", err)
		}
	}

	if gid != nil {
		if err := unix.Setgid(*gid); err != nil {
			return fmt.Errorf("setgid(%d): %w", *gid, err)
		}
	}

	if uid != nil {
		if err := unix.Setuid(*uid); err != nil {
			return fmt.Errorf("setuid(%d): %w", *uid, err)
		}
	}

	return nil
}

func resolveIdentities(cfg config.Config) (*int, *int, []int, error) {
	var (
		uid *int
		gid *int
	)

	if cfg.RunAsUser != "" {
		resolvedUID, resolvedUser, err := resolveUserID(cfg.RunAsUser)
		if err != nil {
			return nil, nil, nil, err
		}

		uid = &resolvedUID

		resolvedGroups, err := resolveUserSupplementaryGroups(resolvedUser)
		if err != nil {
			return nil, nil, nil, err
		}

		return uid, gid, resolvedGroups, nil
	}

	if cfg.RunAsGroup != "" {
		resolvedGID, err := resolveGroupID(cfg.RunAsGroup)
		if err != nil {
			return nil, nil, nil, err
		}

		gid = &resolvedGID
	}

	return uid, gid, nil, nil
}

func resolveUserID(value string) (int, *user.User, error) {
	if numeric, err := strconv.Atoi(value); err == nil {
		u, lookupErr := user.LookupId(strconv.Itoa(numeric))
		if lookupErr != nil {
			return numeric, nil, nil
		}

		return numeric, u, nil
	}

	u, err := user.Lookup(value)
	if err != nil {
		return 0, nil, fmt.Errorf("resolve run_as_user %q: %w", value, err)
	}

	numeric, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, nil, fmt.Errorf("parse uid %q for user %q: %w", u.Uid, value, err)
	}

	return numeric, u, nil
}

func resolveUserSupplementaryGroups(u *user.User) ([]int, error) {
	if u == nil {
		return nil, nil
	}

	groupIDs, err := u.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("resolve supplementary groups for user %q: %w", u.Username, err)
	}

	groups := make([]int, 0, len(groupIDs))

	seen := make(map[int]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		numeric, convErr := strconv.Atoi(groupID)
		if convErr != nil {
			return nil, fmt.Errorf("parse supplementary group id %q for user %q: %w", groupID, u.Username, convErr)
		}

		if _, ok := seen[numeric]; ok {
			continue
		}

		seen[numeric] = struct{}{}
		groups = append(groups, numeric)
	}

	sort.Ints(groups)

	return groups, nil
}

func resolveGroupID(value string) (int, error) {
	if numeric, err := strconv.Atoi(value); err == nil {
		return numeric, nil
	}

	g, err := user.LookupGroup(value)
	if err != nil {
		return 0, fmt.Errorf("resolve group %q: %w", value, err)
	}

	numeric, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("parse gid %q for group %q: %w", g.Gid, value, err)
	}

	return numeric, nil
}

func applyChroot(root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve chroot path %q: %w", root, err)
	}

	if err = unix.Chroot(absRoot); err != nil {
		return fmt.Errorf("chroot %q failed: %w", absRoot, err)
	}

	if err = os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir after chroot: %w", err)
	}

	requiredFiles := []string{"/etc/hosts", "/etc/resolv.conf", "/etc/nsswitch.conf"}
	for _, requiredFile := range requiredFiles {
		if _, statErr := os.Stat(requiredFile); statErr != nil {
			return fmt.Errorf("chroot %q active, but required runtime file missing (%s: %s). Ensure chroot contains /etc/hosts, /etc/resolv.conf and /etc/nsswitch.conf: %w", absRoot, requiredFile, strings.TrimSpace(statErr.Error()), statErr)
		}
	}

	return nil
}

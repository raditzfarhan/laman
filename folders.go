package main

import (
	"errors"
	"fmt"
	"os"

	"laman/internal/sites"
)

const foldersUsage = `usage:
  laman folders                  list your project folders
  laman folders add [path]       serve every folder inside it as a site (default: the current folder)
  laman folders remove [path]    stop serving it (nothing on disk is touched)
  laman folders prefix <prefix|off> [path]
                                 name its sites <prefix>-<folder>.test (acme-api.test)`

// cmdFolders manages the project folders: every folder inside one of them
// is served as <name>.<tld>.
func cmdFolders(args []string) error {
	cfg, err := loadOrInit()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		list, clashes := sites.DiscoverAll(cfg.Folders(), cfg.TLD, nil)
		for i, f := range cfg.Folders() {
			dir := f.Dir
			n := 0
			for _, s := range list {
				if s.Folder == dir {
					n++
				}
			}
			main := ""
			if i == 0 {
				main = "  (main)"
			}
			missing := ""
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				missing = "  (missing)"
			}
			named := ""
			if f.Prefix != "" {
				named = fmt.Sprintf("  (named %s-<folder>.%s)", f.Prefix, cfg.TLD)
			}
			fmt.Printf("%-50s %3d site%s%s%s%s\n", dir, n, plural(n), main, named, missing)
		}
		for _, c := range clashes {
			fmt.Printf("\nNot served: %s (%s.%s is %s)", c.Skipped, c.Name, cfg.TLD, c.Served)
		}
		if len(clashes) > 0 {
			fmt.Println("\nRename one of them, or serve it under another name with `laman link <name>` in that folder.")
		}
		return nil
	}
	if args[0] == "prefix" {
		if len(args) < 2 || len(args) > 3 {
			return errors.New(foldersUsage)
		}
		prefix, dir := args[1], "."
		if len(args) == 3 {
			dir = args[2]
		}
		if prefix == "off" || prefix == "none" {
			prefix = ""
		}
		abs, p, err := cfg.SetFolderPrefix(dir, prefix)
		if err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		if p == "" {
			fmt.Printf("Sites in %s are named after their folders again (<folder>.%s).\n", abs, cfg.TLD)
		} else {
			fmt.Printf("Sites in %s are now named %s-<folder>.%s.\n", abs, p, cfg.TLD)
		}
		return notifyDaemonErr()
	}
	if len(args) > 2 {
		return errors.New(foldersUsage)
	}
	dir := "."
	if len(args) == 2 {
		dir = args[1]
	}
	switch args[0] {
	case "add":
		abs, err := cfg.AddSitesDir(dir)
		if err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		n := 0
		if entries, err := os.ReadDir(abs); err == nil {
			for _, e := range entries {
				if e.IsDir() && sites.Slug(e.Name()) != "" && e.Name()[0] != '.' {
					n++
				}
			}
		}
		name := "<name>"
		folders := cfg.Folders()
		if f := folders[len(folders)-1]; f.Prefix != "" {
			name = f.Prefix + "-<name>"
			defer fmt.Printf("It sits inside another project folder, so its sites get %q in front. For the plain names: laman folders prefix off \"%s\"\n", f.Prefix, abs)
		}
		fmt.Printf("Added %s (%d folder%s inside). Each one is served as %s.%s; `laman folders` lists them.\n", abs, n, plural(n), name, cfg.TLD)
		return notifyDaemonErr()
	case "remove", "rm":
		abs, err := cfg.RemoveSitesDir(dir)
		if err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Removed %s from your project folders. Its files are untouched.\n", abs)
		return notifyDaemonErr()
	}
	return errors.New(foldersUsage)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

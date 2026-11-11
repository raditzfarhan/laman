package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"laman/internal/config"
	"laman/internal/desktop"
	"laman/internal/install"
)

// Node: plain `node`, `npm` and `npx` come from ~/.laman/node, a junction
// to the default version that sits on the user PATH. `laman node|npm|npx`
// additionally honour a project's .nvmrc or .node-version.

// cmdNode runs node, npm or npx with the Node version for the current
// folder.
func cmdNode(tool string, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	v, from, err := nodeHere(cfg)
	if err != nil {
		return err
	}
	dir := install.NodeDir(v)
	if dir == "" {
		if from != "" {
			return fmt.Errorf("%s asks for Node %s, which is not installed; run `laman install node %s`", from, v, v)
		}
		return errors.New("no Node installed; run `laman install node`")
	}
	// Child processes (npm scripts, Vite) find the same node first.
	os.Setenv("PATH", dir+";"+install.NpmPrefix()+";"+os.Getenv("PATH"))
	node := filepath.Join(dir, "node.exe")
	switch tool {
	case "npm":
		args = append([]string{filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js")}, args...)
	case "npx":
		args = append([]string{filepath.Join(dir, "node_modules", "npm", "bin", "npx-cli.js")}, args...)
	}
	return runPassthrough(node, args...)
}

// nodeHere picks the Node version for the current directory: the nearest
// .nvmrc or .node-version, else the default. from names the file used.
func nodeHere(cfg *config.Config) (version, from string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return cfg.DefaultNode(), "", nil
	}
	version, from = cfg.NodeFor(cwd)
	return version, from, nil
}

func nodeSpec(s string) string { return config.NodeSpec(s) }

// setDefaultNode makes v the default: config, the ~/.laman/node junction,
// and the PATH entries that expose it.
func setDefaultNode(cfg *config.Config, v string) error {
	dir := install.NodeDir(v)
	if dir == "" {
		return fmt.Errorf("Node %s is not installed", v)
	}
	cfg.Node = v
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := desktop.Junction(config.NodeLink(), dir); err != nil {
		return err
	}
	fmt.Printf("Default Node is now %s (`node`, `npm` and `npx` in new terminals)\n", v)
	return ensureNodePath()
}

// ensureNodePath puts the Node junction and the npm global folder on the
// user PATH, and warns when another Node would be found first.
func ensureNodePath() error {
	added := false
	for _, dir := range []string{config.NodeLink(), install.NpmPrefix()} {
		a, err := addToUserPath(dir)
		if err != nil {
			return fmt.Errorf("add %s to PATH: %w", dir, err)
		}
		added = added || a
	}
	if added {
		fmt.Println("Added laman's Node to your PATH. Open a new terminal to use it.")
	}
	if other := firstNodeOnPath(); other != "" {
		fmt.Printf("Note: %s comes earlier on your PATH, so plain `node` still runs that one.\n"+
			"Remove it (or uninstall that Node/nvm) to let laman manage Node; `laman node` and `laman npm` use laman's either way.\n", other)
	}
	return nil
}

// firstNodeOnPath returns a folder with node.exe that new terminals would
// search before laman's, or "".
func firstNodeOnPath() string {
	link := strings.ToLower(filepath.Clean(config.NodeLink()))
	for _, dir := range savedPathDirs() {
		clean := filepath.Clean(dir)
		if strings.ToLower(clean) == link {
			return ""
		}
		if _, err := os.Stat(filepath.Join(clean, "node.exe")); err == nil {
			return clean
		}
	}
	return ""
}

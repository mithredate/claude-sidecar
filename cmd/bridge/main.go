package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/term"
)

const version = "0.1.0"

func main() {
	var (
		showHelp     bool
		showVersion  bool
		configPath   string
		initWrappers string
	)

	flag.BoolVar(&showHelp, "help", false, "Show this help message")
	flag.BoolVar(&showHelp, "h", false, "Show this help message (shorthand)")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.BoolVar(&showVersion, "v", false, "Show version (shorthand)")
	flag.StringVar(&configPath, "config", "", "Path to bridge config file")
	flag.StringVar(&configPath, "c", "", "Path to bridge config file (shorthand)")
	flag.StringVar(&initWrappers, "init-wrappers", "", "Generate dispatcher symlinks in specified directory")

	flag.Usage = printUsage
	flag.Parse()

	if showHelp {
		printUsage()
		os.Exit(0)
	}

	if showVersion {
		fmt.Printf("bridge version %s\n", version)
		os.Exit(0)
	}

	// gen-overlay is a self-contained subcommand: it reads its full spec from
	// stdin and doesn't need .sidecar/bridge.yaml. Handle it before LoadConfig
	// so callers (the host wrapper, running in an ephemeral container) don't
	// have to provide a bridge config file.
	if posArgs := flag.Args(); len(posArgs) > 0 && posArgs[0] == "gen-overlay" {
		os.Exit(runGenOverlay(os.Stdin, os.Stdout, os.Stderr))
	}

	// Load config
	config, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	// Handle --init-wrappers flag
	if initWrappers != "" {
		exitCode := initWrappersCommand(config, initWrappers)
		os.Exit(exitCode)
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no command specified")
		fmt.Fprintln(os.Stderr, "Run 'bridge --help' for usage")
		os.Exit(1)
	}

	// Route and execute the command
	exitCode := runCommand(config, args)
	os.Exit(exitCode)
}

// runCommand routes and executes the given command based on config.
// Returns the exit code from the executed command.
func runCommand(config *Config, args []string) int {
	cmdName := args[0]
	cmdArgs := args[1:]

	// Look up command in config
	cmd, found := config.Commands[cmdName]

	if !found {
		// Command not in config and no override - fall through to native lookup
		nativePath, err := exec.LookPath(cmdName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: command '%s' not found in config and not available natively\n", cmdName)
			return 127 // Standard "command not found" exit code
		}
		return execNative(nativePath, args)
	}

	// Resolve container name (apply containers mapping)
	containerName := config.ResolveContainer(cmd.Container)

	// Determine the actual executable (use exec if set, otherwise cmdName)
	executable := cmd.Exec

	// Translate path arguments
	translatedArgs := cmd.TranslateArgs(cmdArgs)

	// Build docker exec command
	// Use -i for interactive mode (keeps stdin open)
	// Use -t for TTY allocation when both stdin and stdout are terminals (for colored output)
	dockerArgs := []string{"exec", "-i"}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		dockerArgs = append(dockerArgs, "-t")
	}

	// Determine working directory for docker exec
	// Priority: 1) Translated CWD, 2) Static workdir from config, 3) Current CWD
	workdir := determineWorkdir(&cmd)
	dockerArgs = append(dockerArgs, "-w", workdir)

	// Add container name
	dockerArgs = append(dockerArgs, containerName)

	// Add the command and its arguments
	dockerArgs = append(dockerArgs, executable)
	dockerArgs = append(dockerArgs, translatedArgs...)

	// Execute docker command
	dockerCmd := exec.Command("docker", dockerArgs...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr

	err := dockerCmd.Run()
	if err != nil {
		// Check for exit error to get exit code
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		// Other error (docker not found, etc.)
		fmt.Fprintf(os.Stderr, "Error: failed to execute docker: %s\n", err)
		return 1
	}

	return 0
}

// determineWorkdir determines the working directory to use for docker exec.
// Priority: 1) Translated CWD (if a path mapping matches)
//  2. Static workdir from config (if set and no mapping matched)
//  3. Current CWD (as fallback)
func determineWorkdir(cmd *Command) string {
	cwd, err := os.Getwd()
	if err != nil {
		// If we can't get CWD, fall back to config workdir or root
		if cmd.Workdir != "" {
			return cmd.Workdir
		}
		return "/"
	}

	// Try to translate the current working directory
	translatedCwd, matched := cmd.TranslatePathWithMatch(cwd)

	// If a path mapping matched, use the translated path (even if same as original)
	if matched {
		return translatedCwd
	}

	// No mapping matched - use static workdir if set, otherwise use CWD
	if cmd.Workdir != "" {
		return cmd.Workdir
	}
	return cwd
}

// execNative executes a native binary using syscall.Exec, replacing the current process.
// If syscall.Exec fails, it returns an error exit code.
// The args parameter should include the command name as the first element (argv[0]).
func execNative(execPath string, args []string) int {
	// syscall.Exec replaces the current process, so this function only returns on error
	err := syscall.Exec(execPath, args, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to exec '%s': %s\n", execPath, err)
		return 1
	}
	// This line is never reached because syscall.Exec replaces the process
	return 0
}

// initWrappersCommand generates dispatcher symlinks for all configured commands.
// It creates symlinks in the specified directory, pointing to the dispatcher script.
// Returns 0 on success, 1 on error.
func initWrappersCommand(config *Config, dir string) int {
	created, skipped, err := initWrappers(config, dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Created %d symlinks in %s", created, dir)
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, " (%d already existed)", skipped)
	}
	fmt.Fprintln(os.Stderr)
	return 0
}

// initWrappers creates dispatcher symlinks for all commands in config.
// Returns (created count, skipped count, error).
func initWrappers(config *Config, dir string) (int, int, error) {
	dispatcherPath := filepath.Join(dir, "dispatcher")

	// Verify dispatcher exists
	if _, err := os.Stat(dispatcherPath); os.IsNotExist(err) {
		return 0, 0, fmt.Errorf("dispatcher not found at %s", dispatcherPath)
	}

	// Collect all command names
	commandNames := make(map[string]bool)
	for name := range config.Commands {
		commandNames[name] = true
	}

	created := 0
	skipped := 0

	for name := range commandNames {
		symlinkPath := filepath.Join(dir, name)

		// Check if symlink already exists and points to dispatcher
		if target, err := os.Readlink(symlinkPath); err == nil {
			// Symlink exists - check if it points to dispatcher
			if target == "dispatcher" || target == dispatcherPath {
				skipped++
				continue
			}
			// Symlink exists but points elsewhere - remove it
			if err := os.Remove(symlinkPath); err != nil {
				return created, skipped, fmt.Errorf("failed to remove existing symlink %s: %w", symlinkPath, err)
			}
		} else if !os.IsNotExist(err) {
			// File exists but is not a symlink - check if it's a regular file
			if _, statErr := os.Stat(symlinkPath); statErr == nil {
				// Skip non-symlink files (e.g., the dispatcher itself)
				skipped++
				continue
			}
		}

		// Create symlink pointing to dispatcher (relative path)
		if err := os.Symlink("dispatcher", symlinkPath); err != nil {
			return created, skipped, fmt.Errorf("failed to create symlink %s: %w", symlinkPath, err)
		}
		created++
	}

	return created, skipped, nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `bridge - Execute commands in sidecar containers

Usage:
  bridge [flags] <command> [args...]
  bridge --init-wrappers <dir>

Flags:
  -c, --config string        Path to bridge config file (default: $SIDECAR_CONFIG_DIR/bridge.yaml)
  -h, --help                 Show this help message
  -v, --version              Show version
  --init-wrappers <dir>      Generate dispatcher symlinks in specified directory

Examples:
  bridge npm install           Run npm install in the default container
  bridge php artisan migrate   Run php artisan migrate in the PHP container
  bridge --config ./my.yaml npm test
  bridge --init-wrappers /scripts/wrappers   Generate symlinks at startup

The bridge reads configuration from $SIDECAR_CONFIG_DIR/bridge.yaml (or BRIDGE_CONFIG env var).
SIDECAR_CONFIG_DIR defaults to $PWD/.sidecar if not set.
`)
}

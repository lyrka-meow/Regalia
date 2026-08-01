package apps

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Application struct {
	DesktopID   string `json:"desktopId"`
	Name        string `json:"name"`
	Icon        string `json:"icon,omitempty"`
	ProcessPath string `json:"processPath"`
}

func List() []Application {
	seen := map[string]bool{}
	running := runningProcessPaths()
	var applications []Application
	for _, directory := range applicationDirectories() {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".desktop") || seen[entry.Name()] {
				continue
			}
			application, ok := parseDesktopFile(filepath.Join(directory, entry.Name()), entry.Name(), running)
			if !ok {
				continue
			}
			seen[entry.Name()] = true
			applications = append(applications, application)
		}
	}
	sort.Slice(applications, func(i, j int) bool {
		return strings.ToLower(applications[i].Name) < strings.ToLower(applications[j].Name)
	})
	return applications
}

func applicationDirectories() []string {
	var directories []string
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		directories = append(directories, filepath.Join(dataHome, "applications"))
	} else if home, err := os.UserHomeDir(); err == nil {
		directories = append(directories, filepath.Join(home, ".local", "share", "applications"))
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, directory := range filepath.SplitList(dataDirs) {
		directories = append(directories, filepath.Join(directory, "applications"))
	}
	return directories
}

func parseDesktopFile(path, desktopID string, running map[string]string) (Application, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Application{}, false
	}
	defer file.Close()

	values := map[string]string{}
	inDesktopEntry := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inDesktopEntry = line == "[Desktop Entry]"
			continue
		}
		if !inDesktopEntry || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.Contains(key, "[") {
			continue
		}
		values[key] = value
	}
	if values["Type"] != "" && values["Type"] != "Application" {
		return Application{}, false
	}
	if strings.EqualFold(values["NoDisplay"], "true") || strings.EqualFold(values["Hidden"], "true") {
		return Application{}, false
	}

	command := values["TryExec"]
	if command == "" {
		command = applicationExecToken(values["Exec"])
	}
	executable := resolveExecutable(command)
	if values["Name"] == "" || executable == "" {
		return Application{}, false
	}
	return Application{
		DesktopID:   desktopID,
		Name:        values["Name"],
		Icon:        values["Icon"],
		ProcessPath: resolveProcessPath(executable, running),
	}, true
}

// PathsByDesktopID returns the executable path sing-box will actually see for
// every installed desktop application. Desktop launchers are often only small
// wrappers (for example /usr/bin/chromium starts /usr/lib/chromium/chromium),
// so using the Exec= path verbatim makes process_path routing silently miss.
func PathsByDesktopID() map[string]string {
	paths := map[string]string{}
	for _, application := range List() {
		paths[application.DesktopID] = application.ProcessPath
	}
	return paths
}

func firstExecToken(command string) string {
	tokens := execTokens(command)
	if len(tokens) == 0 {
		return ""
	}
	return tokens[0]
}

func execTokens(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	var tokens []string
	var token strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		value := token.String()
		token.Reset()
		if value != "" && !strings.HasPrefix(value, "%") {
			tokens = append(tokens, value)
		}
	}
	for _, character := range command {
		if escaped {
			token.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				token.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == ' ' || character == '\t' {
			flush()
			continue
		}
		token.WriteRune(character)
	}
	flush()
	return tokens
}

func applicationExecToken(command string) string {
	tokens := execTokens(command)
	for len(tokens) > 0 {
		name := filepath.Base(tokens[0])
		switch name {
		case "prime-run", "gamemoderun", "nohup", "command", "exec":
			tokens = tokens[1:]
			continue
		case "env":
			tokens = tokens[1:]
			for len(tokens) > 0 {
				if strings.Contains(tokens[0], "=") {
					tokens = tokens[1:]
					continue
				}
				if tokens[0] == "-u" || tokens[0] == "--unset" || tokens[0] == "-C" || tokens[0] == "--chdir" {
					if len(tokens) < 2 {
						return ""
					}
					tokens = tokens[2:]
					continue
				}
				if strings.HasPrefix(tokens[0], "-") {
					tokens = tokens[1:]
					continue
				}
				break
			}
			continue
		case "sh", "bash":
			if len(tokens) >= 3 && tokens[1] == "-c" {
				return applicationExecToken(tokens[2])
			}
		}
		break
	}
	if len(tokens) == 0 || strings.HasPrefix(tokens[0], "%") {
		return ""
	}
	return tokens[0]
}

func resolveExecutable(command string) string {
	if command == "" {
		return ""
	}
	path := command
	if !filepath.IsAbs(path) {
		var err error
		path, err = exec.LookPath(command)
		if err != nil {
			return ""
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return path
}

func resolveProcessPath(launcher string, running map[string]string) string {
	if processPath := running[strings.ToLower(filepath.Base(launcher))]; processPath != "" && processPath != launcher {
		return processPath
	}
	if processPath := packagedProcessPath(launcher); processPath != "" {
		return processPath
	}
	return launcher
}

func runningProcessPaths() map[string]string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return map[string]string{}
	}
	counts := map[string]map[string]int{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		processPath, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		processPath = strings.TrimSuffix(processPath, " (deleted)")
		if executableFile(processPath) {
			base := strings.ToLower(filepath.Base(processPath))
			if counts[base] == nil {
				counts[base] = map[string]int{}
			}
			counts[base][processPath]++
		}
	}
	paths := map[string]string{}
	for base, candidates := range counts {
		for processPath, count := range candidates {
			best := paths[base]
			if best == "" || count > candidates[best] || count == candidates[best] && processPath < best {
				paths[base] = processPath
			}
		}
	}
	return paths
}

func packagedProcessPath(launcher string) string {
	base := filepath.Base(launcher)
	for _, root := range []string{
		filepath.Join("/usr/lib", base),
		filepath.Join("/usr/lib64", base),
		filepath.Join("/opt", base),
	} {
		if processPath := matchingExecutable(root, base, 2); processPath != "" {
			return processPath
		}
	}
	return ""
}

func matchingExecutable(root, base string, depth int) string {
	if depth < 0 {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		candidate := filepath.Join(root, entry.Name())
		if !entry.IsDir() && strings.EqualFold(entry.Name(), base) && executableFile(candidate) {
			return candidate
		}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if candidate := matchingExecutable(filepath.Join(root, entry.Name()), base, depth-1); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

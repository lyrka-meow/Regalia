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
	DesktopID    string `json:"desktopId"`
	Name         string `json:"name"`
	Icon         string `json:"icon,omitempty"`
	LauncherPath string `json:"launcherPath"`
}

type Process struct {
	Name         string `json:"name"`
	ProcessPath  string `json:"processPath"`
	ProcessCount int    `json:"processCount"`
	AppImage     bool   `json:"appImage"`
}

func List() []Application {
	seen := map[string]bool{}
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
			application, ok := parseDesktopFile(filepath.Join(directory, entry.Name()), entry.Name())
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

// Processes returns executable paths read directly from /proc/PID/exe. Unlike
// desktop Exec= commands, these are the paths the kernel and sing-box observe
// for running applications. Entries sharing an executable are grouped so the
// shell can present one exact path instead of dozens of Chromium subprocesses.
func Processes() []Process {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return []Process{}
	}
	grouped := map[string]*Process{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		processDirectory := filepath.Join("/proc", entry.Name())
		processPath, err := os.Readlink(filepath.Join(processDirectory, "exe"))
		if err != nil {
			continue
		}
		processPath = strings.TrimSuffix(processPath, " (deleted)")
		if !filepath.IsAbs(processPath) {
			continue
		}
		name := strings.TrimSpace(readText(filepath.Join(processDirectory, "comm")))
		if name == "" {
			name = filepath.Base(processPath)
		}
		candidate := grouped[processPath]
		if candidate == nil {
			candidate = &Process{
				Name:        name,
				ProcessPath: processPath,
				AppImage:    isAppImage(processPath),
			}
			grouped[processPath] = candidate
		}
		candidate.ProcessCount++
	}
	processes := make([]Process, 0, len(grouped))
	for _, process := range grouped {
		processes = append(processes, *process)
	}
	sort.Slice(processes, func(i, j int) bool {
		left := strings.ToLower(processes[i].Name + "\x00" + processes[i].ProcessPath)
		right := strings.ToLower(processes[j].Name + "\x00" + processes[j].ProcessPath)
		return left < right
	})
	return processes
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

func parseDesktopFile(path, desktopID string) (Application, bool) {
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
		DesktopID:    desktopID,
		Name:         values["Name"],
		Icon:         values["Icon"],
		LauncherPath: executable,
	}, true
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

func readText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func isAppImage(processPath string) bool {
	lowerPath := strings.ToLower(processPath)
	return strings.Contains(lowerPath, "/.mount_") ||
		strings.HasSuffix(lowerPath, ".appimage")
}

package apps

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
		command = firstExecToken(values["Exec"])
	}
	executable := resolveExecutable(command)
	if values["Name"] == "" || executable == "" {
		return Application{}, false
	}
	return Application{
		DesktopID:   desktopID,
		Name:        values["Name"],
		Icon:        values["Icon"],
		ProcessPath: executable,
	}, true
}

func firstExecToken(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	var token strings.Builder
	var quote rune
	escaped := false
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
			break
		}
		token.WriteRune(character)
	}
	value := token.String()
	if strings.HasPrefix(value, "%") {
		return ""
	}
	return value
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

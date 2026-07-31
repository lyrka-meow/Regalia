package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lyrka-meow/Regalia/internal/client"
	"github.com/lyrka-meow/Regalia/internal/paths"
)

type terminal struct {
	api    *client.Client
	reader *bufio.Reader
}

type profileView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	LastError   string `json:"lastError"`
	ServerCount int    `json:"serverCount"`
}

type serverView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Ready    bool   `json:"ready"`
}

func main() {
	app := &terminal{
		api:    client.New(paths.Socket()),
		reader: bufio.NewReader(os.Stdin),
	}
	if len(os.Args) > 1 {
		if err := app.runCommand(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "regalia:", err)
			os.Exit(1)
		}
		return
	}
	app.runTUI()
}

func (t *terminal) runCommand(arguments []string) error {
	if len(arguments) == 0 {
		return nil
	}
	switch arguments[0] {
	case "status":
		return t.printMethod("status", nil)
	case "apps":
		return t.printMethod("apps.list", nil)
	case "profiles":
		return t.printMethod("profiles.list", nil)
	case "servers":
		return t.printMethod("servers.list", nil)
	case "routes":
		return t.printMethod("routes.list", nil)
	case "profile":
		return t.profileCommand(arguments[1:])
	case "server":
		return t.serverCommand(arguments[1:])
	default:
		return fmt.Errorf("unknown command %q\n\n%s", arguments[0], commandHelp())
	}
}

func (t *terminal) profileCommand(arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("profile command is required: add, refresh, or delete")
	}
	switch arguments[0] {
	case "add":
		if len(arguments) != 3 {
			return fmt.Errorf("usage: regalia profile add NAME URL")
		}
		return t.printMethod("profiles.create", map[string]string{
			"name":            arguments[1],
			"subscriptionUrl": arguments[2],
		})
	case "refresh":
		if len(arguments) != 2 {
			return fmt.Errorf("usage: regalia profile refresh PROFILE_ID")
		}
		return t.printMethod("profiles.refresh", map[string]string{"id": arguments[1]})
	case "delete":
		if len(arguments) != 2 {
			return fmt.Errorf("usage: regalia profile delete PROFILE_ID")
		}
		return t.printMethod("profiles.delete", map[string]string{"id": arguments[1]})
	default:
		return fmt.Errorf("unknown profile command %q", arguments[0])
	}
}

func (t *terminal) serverCommand(arguments []string) error {
	if len(arguments) != 2 || arguments[0] != "select" {
		return fmt.Errorf("usage: regalia server select SERVER_ID")
	}
	return t.printMethod("servers.select", map[string]string{"id": arguments[1]})
}

func (t *terminal) printMethod(method string, params any) error {
	raw, err := t.api.Call(method, params)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))
	return nil
}

func (t *terminal) runTUI() {
	for {
		clear()
		fmt.Println("╭────────────────────────────────╮")
		fmt.Println("│             REGALIA              │")
		fmt.Println("│   local VPN control center     │")
		fmt.Println("╰────────────────────────────────╯")
		fmt.Println()
		t.printStatus()
		fmt.Println()
		fmt.Println("  1) Subscription profiles")
		fmt.Println("  2) Servers")
		fmt.Println("  3) Route profiles")
		fmt.Println("  4) Installed applications")
		fmt.Println("  0) Exit")
		switch t.ask("\nChoice") {
		case "1":
			t.profilesMenu()
		case "2":
			t.serversMenu()
		case "3":
			t.showAndWait("routes.list", nil)
		case "4":
			t.showAndWait("apps.list", nil)
		case "0":
			return
		}
	}
}

func (t *terminal) profilesMenu() {
	for {
		clear()
		profiles, err := t.profiles()
		if err != nil {
			t.errorAndWait(err)
			return
		}
		fmt.Println("SUBSCRIPTION PROFILES")
		fmt.Println(strings.Repeat("─", 48))
		if len(profiles) == 0 {
			fmt.Println("No subscription profiles yet.")
		}
		for index, profile := range profiles {
			status := fmt.Sprintf("%d servers", profile.ServerCount)
			if profile.LastError != "" {
				status = "update failed"
			}
			fmt.Printf("%2d) %-24s %s\n", index+1, truncate(profile.Name, 24), status)
		}
		fmt.Println()
		fmt.Println("  1) Add subscription")
		fmt.Println("  2) Refresh subscription")
		fmt.Println("  3) Delete subscription")
		fmt.Println("  0) Back")
		switch t.ask("\nChoice") {
		case "1":
			name := t.ask("Profile name")
			subscriptionURL := t.ask("Subscription URL")
			if name == "" || subscriptionURL == "" {
				continue
			}
			raw, err := t.api.Call("profiles.create", map[string]string{
				"name":            name,
				"subscriptionUrl": subscriptionURL,
			})
			if err != nil {
				t.errorAndWait(err)
				continue
			}
			var profile profileView
			if err := json.Unmarshal(raw, &profile); err != nil {
				t.errorAndWait(err)
				continue
			}
			fmt.Println("\nDownloading subscription…")
			if _, err := t.api.Call("profiles.refresh", map[string]string{"id": profile.ID}); err != nil {
				t.errorAndWait(err)
			}
		case "2":
			if profile, ok := t.chooseProfile(profiles); ok {
				fmt.Println("\nDownloading subscription…")
				if _, err := t.api.Call("profiles.refresh", map[string]string{"id": profile.ID}); err != nil {
					t.errorAndWait(err)
				}
			}
		case "3":
			if profile, ok := t.chooseProfile(profiles); ok {
				if strings.EqualFold(t.ask("Type YES to delete "+profile.Name), "yes") {
					if _, err := t.api.Call("profiles.delete", map[string]string{"id": profile.ID}); err != nil {
						t.errorAndWait(err)
					}
				}
			}
		case "0":
			return
		}
	}
}

func (t *terminal) serversMenu() {
	clear()
	var response struct {
		ActiveServerID string `json:"activeServerId"`
		Profiles       []struct {
			ProfileID   string       `json:"profileId"`
			ProfileName string       `json:"profileName"`
			Items       []serverView `json:"items"`
		} `json:"profiles"`
	}
	raw, err := t.api.Call("servers.list", nil)
	if err != nil {
		t.errorAndWait(err)
		return
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.errorAndWait(err)
		return
	}

	fmt.Println("SERVERS")
	fmt.Println(strings.Repeat("─", 68))
	var servers []serverView
	for _, profile := range response.Profiles {
		if len(profile.Items) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", profile.ProfileName)
		for _, server := range profile.Items {
			servers = append(servers, server)
			marker := " "
			if server.ID == response.ActiveServerID {
				marker = "●"
			}
			readiness := ""
			if !server.Ready {
				readiness = "  not connectable yet"
			}
			fmt.Printf("%s %2d) %-28s %-11s %s%s\n",
				marker, len(servers), truncate(server.Name, 28),
				server.Protocol, address(server), readiness)
		}
	}
	if len(servers) == 0 {
		fmt.Println("No servers. Add or refresh a subscription first.")
		t.wait()
		return
	}
	fmt.Println("\n  0) Back")
	choice, err := strconv.Atoi(t.ask("Select server"))
	if err != nil || choice == 0 {
		return
	}
	if choice < 1 || choice > len(servers) {
		return
	}
	selected := servers[choice-1]
	if !selected.Ready {
		t.errorAndWait(fmt.Errorf("%s support is imported but not connectable yet", selected.Protocol))
		return
	}
	if _, err := t.api.Call("servers.select", map[string]string{"id": selected.ID}); err != nil {
		t.errorAndWait(err)
	}
}

func (t *terminal) profiles() ([]profileView, error) {
	raw, err := t.api.Call("profiles.list", nil)
	if err != nil {
		return nil, err
	}
	var profiles []profileView
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func (t *terminal) chooseProfile(profiles []profileView) (profileView, bool) {
	if len(profiles) == 0 {
		return profileView{}, false
	}
	choice, err := strconv.Atoi(t.ask("Profile number"))
	if err != nil || choice < 1 || choice > len(profiles) {
		return profileView{}, false
	}
	return profiles[choice-1], true
}

func (t *terminal) printStatus() {
	raw, err := t.api.Call("status", nil)
	if err != nil {
		fmt.Println("  Daemon: offline")
		fmt.Println("  Start it with: regaliad")
		return
	}
	var status struct {
		Engine         string `json:"engine"`
		Connected      bool   `json:"connected"`
		Tun            bool   `json:"tun"`
		ActiveServerID string `json:"activeServerId"`
		Configuration  string `json:"configuration"`
		ConfigError    string `json:"configurationError"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		fmt.Println("  Daemon: invalid response")
		return
	}
	fmt.Println("  Daemon: online")
	fmt.Printf("  Engine: %s\n", status.Engine)
	fmt.Printf("  VPN:    %s\n", onOff(status.Connected))
	fmt.Printf("  TUN:    %s\n", onOff(status.Tun))
	fmt.Printf("  Config: %s\n", status.Configuration)
	if status.ActiveServerID != "" {
		fmt.Printf("  Server: %s\n", status.ActiveServerID)
	}
	if status.ConfigError != "" {
		fmt.Printf("  Note:   %s\n", status.ConfigError)
	}
}

func (t *terminal) showAndWait(method string, params any) {
	clear()
	if err := t.printMethod(method, params); err != nil {
		fmt.Println("Error:", err)
	}
	t.wait()
}

func (t *terminal) errorAndWait(err error) {
	fmt.Println("\nError:", err)
	t.wait()
}

func (t *terminal) wait() {
	fmt.Print("\nPress Enter to return…")
	_, _ = t.reader.ReadString('\n')
}

func (t *terminal) ask(label string) string {
	fmt.Print(label + ": ")
	value, _ := t.reader.ReadString('\n')
	return strings.TrimSpace(value)
}

func clear() {
	fmt.Print("\033[2J\033[H")
}

func address(server serverView) string {
	if server.Address == "" {
		return ""
	}
	if server.Port > 0 {
		return fmt.Sprintf("%s:%d", server.Address, server.Port)
	}
	return server.Address
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width < 2 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func commandHelp() string {
	return `commands:
  regalia status
  regalia profiles
  regalia profile add NAME URL
  regalia profile refresh PROFILE_ID
  regalia profile delete PROFILE_ID
  regalia servers
  regalia server select SERVER_ID
  regalia routes
  regalia apps`
}

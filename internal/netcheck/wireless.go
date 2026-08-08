package netcheck

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// WirelessSignalDBm reads the kernel's current link level without spawning a
// helper process. Nil means that the interface is not wireless or the driver
// does not expose Wireless Extensions statistics.
func WirelessSignalDBm(interfaceName string) *float64 {
	file, err := os.Open("/proc/net/wireless")
	if err != nil {
		return nil
	}
	defer file.Close()
	return parseWirelessSignal(file, interfaceName)
}

func parseWirelessSignal(file *os.File, interfaceName string) *float64 {
	if interfaceName == "" {
		return nil
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		separator := strings.IndexByte(line, ':')
		if separator < 0 || strings.TrimSpace(line[:separator]) != interfaceName {
			continue
		}
		fields := strings.Fields(line[separator+1:])
		if len(fields) < 3 {
			return nil
		}
		level, err := strconv.ParseFloat(strings.TrimSuffix(fields[2], "."), 64)
		if err != nil || level > 0 || level < -150 {
			return nil
		}
		return &level
	}
	return nil
}

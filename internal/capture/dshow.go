package capture

import "strings"

// ParseDshowDevices parses the stderr of
// `ffmpeg -list_devices true -f dshow -i dummy` (Windows). ffmpeg exits
// non-zero on this invocation by design; the caller must ignore the exit code
// and parse only the output.
//
// Output shape (prefix hex width varies across ffmpeg 4.x..7.x):
//
//	[dshow @ 0x7ff...] DirectShow video devices (some may be both audio and video)
//	[dshow @ 0x7ff...]  "USB2.0 HD UVC WebCam" (video)
//	[dshow @ 0x7ff...]   Alternative name "@device_pnp_..."
//	[dshow @ 0x7ff...] DirectShow audio devices
//	[dshow @ 0x7ff...]  "Microphone" (audio)
func ParseDshowDevices(stderr string) []Device {
	var devs []Device
	inVideo := false
	seen := map[string]bool{}
	for _, raw := range strings.Split(stderr, "\n") {
		line := stripDshowPrefix(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		switch {
		case strings.Contains(low, "directshow video devices"):
			inVideo = true
			continue
		case strings.Contains(low, "directshow audio devices"):
			inVideo = false
			continue
		}
		if !inVideo || strings.Contains(low, "alternative name") {
			continue
		}
		name := quotedValue(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		devs = append(devs, Device{Index: len(devs), ID: name, Name: name})
	}
	return devs
}

// stripDshowPrefix removes a leading "[dshow @ 0x...]" marker of any width.
func stripDshowPrefix(line string) string {
	if !strings.HasPrefix(line, "[dshow") {
		return line
	}
	if i := strings.Index(line, "]"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return line
}

// quotedValue returns the text between the first pair of double quotes.
func quotedValue(line string) string {
	start := strings.Index(line, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(line[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return line[start+1 : start+1+end]
}

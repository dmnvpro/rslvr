package capture

import "strings"

// ParseDshowDevices parses the stderr of
// `ffmpeg -list_devices true -f dshow -i dummy` (Windows). ffmpeg exits
// non-zero on this invocation by design; the caller must ignore the exit code
// and parse only the output.
//
// Device entries are recognized by a quoted name followed by a
// "(video)" / "(audio)" marker. That shape holds across versions, while the
// surrounding text does not: ffmpeg up to 6.x prints "[dshow @ 0x...]" lines
// with section headers, 7.x+ prints "[in#0 @ 0x...]" and no headers at all:
//
//	[dshow @ 0x7ff...] DirectShow video devices (some may be both audio and video)
//	[dshow @ 0x7ff...]  "USB2.0 HD UVC WebCam" (video)
//	[dshow @ 0x7ff...]   Alternative name "@device_pnp_..."
//	[in#0 @ 0x1d3...] "OBS Virtual Camera" (video)
func ParseDshowDevices(stderr string) []Device {
	var devs []Device
	seen := map[string]bool{}
	for _, raw := range strings.Split(stderr, "\n") {
		line := stripLogPrefix(strings.TrimSpace(raw))
		if line == "" || strings.Contains(strings.ToLower(line), "alternative name") {
			continue
		}
		if deviceKind(line) != "video" {
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

// deviceKind returns the "(video)" / "(audio)" marker that follows the quoted
// device name, or "" for non-device lines.
func deviceKind(line string) string {
	end := strings.LastIndex(line, `"`)
	if end < 0 {
		return ""
	}
	switch strings.TrimSpace(line[end+1:]) {
	case "(video)":
		return "video"
	case "(audio)":
		return "audio"
	default:
		return ""
	}
}

// stripLogPrefix removes a leading bracketed ffmpeg log prefix of any width,
// e.g. "[dshow @ 0x7ff...]" or "[in#0 @ 0x1d3...]".
func stripLogPrefix(line string) string {
	if !strings.HasPrefix(line, "[") {
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

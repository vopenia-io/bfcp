// Package bfcp implements the Binary Floor Control Protocol (BFCP) as defined in RFC 8855 and RFC 8856.
package bfcp

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// SDPParams contains BFCP parameters extracted from SDP (RFC 8856)
type SDPParams struct {
	// Connection details
	ConnectionIP netip.Addr
	Port         uint16

	// BFCP protocol attributes (RFC 8856 Section 5)
	FloorCtrl    string // "c-s", "c-only", "s-only", "s-c" (RFC 8856 Section 5.1)
	ConferenceID uint32 // Conference identifier (RFC 8856 Section 5.2)
	UserID       uint16 // User identifier (RFC 8856 Section 5.3)
	FloorID      uint16 // Floor identifier (RFC 8856 Section 5.4)
	MediaStream  uint16 // mstrm value linking floor to video m-line (RFC 8856 Section 5.4)
	Setup        string // TCP setup role: "active", "passive", "actpass" (RFC 4145)
	Connection   string // TCP connection: "new", "existing" (RFC 4145)
}

// ParseSDPParams extracts BFCP parameters from raw SDP offer
// Returns nil if no BFCP section found (RFC 8856 Section 3)
//
// Example BFCP media section:
//   m=application 16886 TCP/BFCP *
//   c=IN IP4 192.168.0.104
//   a=floorctrl:c-s
//   a=confid:1
//   a=userid:2
//   a=floorid:1 mstrm:3
//   a=setup:actpass
//   a=connection:new
func ParseSDPParams(sdpData []byte) (*SDPParams, error) {
	sdpStr := string(sdpData)
	lines := strings.Split(sdpStr, "\n")

	// Compile regex patterns for SDP parsing
	// RFC 8856 Section 3: m=application <port> TCP/BFCP *
	bfcpMediaLineRe := regexp.MustCompile(`m=application\s+(\d+)\s+TCP(?:/TLS)?/BFCP`)

	// RFC 8856 Section 5 - BFCP-specific attributes
	confIDRe := regexp.MustCompile(`a=confid:(\d+)`)
	userIDRe := regexp.MustCompile(`a=userid:(\d+)`)
	floorIDRe := regexp.MustCompile(`a=floorid:(\d+)(?:\s+mstrm:(\d+))?`)
	floorCtrlRe := regexp.MustCompile(`a=floorctrl:([\w-]+)`)

	// RFC 4145 - TCP connection attributes
	setupRe := regexp.MustCompile(`a=setup:([\w-]+)`)
	connectionRe := regexp.MustCompile(`a=connection:([\w-]+)`)

	// RFC 4566 Section 5.7 - Connection data
	connectionIPRe := regexp.MustCompile(`c=IN IP4 ([\d.]+)`)

	var params *SDPParams
	var lastConnectionIP netip.Addr
	inBFCPMedia := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Track connection IP from c= lines (can be at session or media level)
		if matches := connectionIPRe.FindStringSubmatch(line); matches != nil {
			if addr, err := netip.ParseAddr(matches[1]); err == nil {
				lastConnectionIP = addr
			}
		}

		// Detect BFCP media section (RFC 8856 Section 3)
		if matches := bfcpMediaLineRe.FindStringSubmatch(line); matches != nil {
			port, _ := strconv.ParseUint(matches[1], 10, 16)
			params = &SDPParams{
				Port:         uint16(port),
				ConnectionIP: lastConnectionIP,
			}
			inBFCPMedia = true
			continue
		}

		// Parse BFCP attributes (only within BFCP media section)
		if inBFCPMedia {
			// Stop parsing when we hit next media section
			if strings.HasPrefix(line, "m=") && !strings.Contains(line, "application") {
				inBFCPMedia = false
				continue
			}

			// Update connection IP if c= line appears in BFCP media section (overrides session-level)
			if matches := connectionIPRe.FindStringSubmatch(line); matches != nil {
				if addr, err := netip.ParseAddr(matches[1]); err == nil {
					params.ConnectionIP = addr
				}
			}

			// Parse BFCP-specific attributes (RFC 8856 Section 5)
			if matches := confIDRe.FindStringSubmatch(line); matches != nil {
				confID, _ := strconv.ParseUint(matches[1], 10, 32)
				params.ConferenceID = uint32(confID)
			} else if matches := userIDRe.FindStringSubmatch(line); matches != nil {
				userID, _ := strconv.ParseUint(matches[1], 10, 16)
				params.UserID = uint16(userID)
			} else if matches := floorIDRe.FindStringSubmatch(line); matches != nil {
				floorID, _ := strconv.ParseUint(matches[1], 10, 16)
				params.FloorID = uint16(floorID)

				// Extract optional mstrm (media stream ID)
				if len(matches) > 2 && matches[2] != "" {
					mstrm, _ := strconv.ParseUint(matches[2], 10, 16)
					params.MediaStream = uint16(mstrm)
				}
			} else if matches := floorCtrlRe.FindStringSubmatch(line); matches != nil {
				params.FloorCtrl = matches[1]
			} else if matches := setupRe.FindStringSubmatch(line); matches != nil {
				params.Setup = matches[1]
			} else if matches := connectionRe.FindStringSubmatch(line); matches != nil {
				params.Connection = matches[1]
			}
		}
	}

	if params == nil {
		// No BFCP media section found
		return nil, nil
	}

	// Validate required fields
	if !params.ConnectionIP.IsValid() {
		return nil, fmt.Errorf("BFCP connection IP not found or invalid")
	}
	if params.Port == 0 {
		return nil, fmt.Errorf("BFCP port is zero")
	}

	return params, nil
}

// ValidateSDPParams validates the parsed BFCP SDP parameters
// Returns a list of validation warnings (non-fatal issues)
func ValidateSDPParams(params *SDPParams) []string {
	if params == nil {
		return nil
	}

	var warnings []string

	// Validate floor control mode (RFC 8856 Section 5.1)
	validFloorCtrl := map[string]bool{
		"c-s":    true, // client-server (most common for Poly)
		"c-only": true, // client-only
		"s-only": true, // server-only
		"s-c":    true, // server-client (rare)
	}
	if params.FloorCtrl != "" && !validFloorCtrl[params.FloorCtrl] {
		warnings = append(warnings, fmt.Sprintf("unexpected floor control mode: %s (expected: c-s, c-only, s-only, or s-c)", params.FloorCtrl))
	}

	// Validate TCP setup role (RFC 4145)
	validSetup := map[string]bool{
		"active":  true,
		"passive": true,
		"actpass": true,
	}
	if params.Setup != "" && !validSetup[params.Setup] {
		warnings = append(warnings, fmt.Sprintf("unexpected TCP setup role: %s (expected: active, passive, or actpass)", params.Setup))
	}

	// Warn if essential attributes are missing or zero
	if params.ConferenceID == 0 {
		warnings = append(warnings, "conference ID is 0 - may be invalid")
	}
	if params.UserID == 0 {
		warnings = append(warnings, "user ID is 0 - may be invalid")
	}
	if params.FloorID == 0 {
		warnings = append(warnings, "floor ID is 0 - may be invalid")
	}

	return warnings
}

// ServerAddr returns the BFCP server address as "ip:port"
func (p *SDPParams) ServerAddr() string {
	if p == nil || !p.ConnectionIP.IsValid() {
		return ""
	}
	return fmt.Sprintf("%s:%d", p.ConnectionIP.String(), p.Port)
}

// String returns a human-readable representation of the BFCP SDP parameters
func (p *SDPParams) String() string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("BFCP{addr=%s, confID=%d, userID=%d, floorID=%d, floorCtrl=%s, mstrm=%d, setup=%s}",
		p.ServerAddr(), p.ConferenceID, p.UserID, p.FloorID, p.FloorCtrl, p.MediaStream, p.Setup)
}

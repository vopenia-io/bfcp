package bfcp

import (
	"net/netip"
	"testing"
)

func TestParseSDPParams(t *testing.T) {
	tests := []struct {
		name           string
		sdp            string
		wantParams     *SDPParams
		wantErr        bool
		wantValidation []string
	}{
		{
			name: "Poly device SDP with BFCP",
			sdp: `v=0
o=- 123456 123456 IN IP4 192.168.0.100
s=Conference
c=IN IP4 192.168.0.100
t=0 0
m=audio 5004 RTP/AVP 0 8 18 101
a=rtpmap:0 PCMU/8000
a=rtpmap:8 PCMA/8000
m=video 5006 RTP/AVP 96
a=rtpmap:96 H264/90000
m=application 16886 TCP/BFCP *
c=IN IP4 192.168.0.104
a=floorctrl:c-s
a=confid:1
a=userid:2
a=floorid:1 mstrm:3
a=setup:actpass
a=connection:new
`,
			wantParams: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.104"),
				Port:         16886,
				FloorCtrl:    "c-s",
				ConferenceID: 1,
				UserID:       2,
				FloorID:      1,
				MediaStream:  3,
				Setup:        "actpass",
				Connection:   "new",
			},
			wantErr: false,
		},
		{
			name: "SDP without BFCP",
			sdp: `v=0
o=- 123456 123456 IN IP4 192.168.0.100
s=Conference
c=IN IP4 192.168.0.100
t=0 0
m=audio 5004 RTP/AVP 0
a=rtpmap:0 PCMU/8000
m=video 5006 RTP/AVP 96
a=rtpmap:96 H264/90000
`,
			wantParams: nil,
			wantErr:    false,
		},
		{
			name: "BFCP with TLS",
			sdp: `v=0
o=- 123456 123456 IN IP4 192.168.0.100
s=Conference
c=IN IP4 192.168.0.100
t=0 0
m=application 50000 TCP/TLS/BFCP *
c=IN IP4 10.0.0.50
a=floorctrl:c-only
a=confid:42
a=userid:100
a=floorid:5
a=setup:active
`,
			wantParams: &SDPParams{
				ConnectionIP: netip.MustParseAddr("10.0.0.50"),
				Port:         50000,
				FloorCtrl:    "c-only",
				ConferenceID: 42,
				UserID:       100,
				FloorID:      5,
				MediaStream:  0,
				Setup:        "active",
				Connection:   "",
			},
			wantErr: false,
		},
		{
			name: "BFCP without optional mstrm",
			sdp: `v=0
o=- 123456 123456 IN IP4 192.168.0.100
s=Conference
c=IN IP4 192.168.0.100
t=0 0
m=application 5070 TCP/BFCP *
a=floorctrl:s-only
a=confid:99
a=userid:1
a=floorid:2
`,
			wantParams: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.100"),
				Port:         5070,
				FloorCtrl:    "s-only",
				ConferenceID: 99,
				UserID:       1,
				FloorID:      2,
				MediaStream:  0,
				Setup:        "",
				Connection:   "",
			},
			wantErr: false,
		},
		{
			name: "BFCP with zero values (validation warnings expected)",
			sdp: `v=0
o=- 123456 123456 IN IP4 192.168.0.100
s=Conference
c=IN IP4 192.168.0.100
t=0 0
m=application 5070 TCP/BFCP *
a=floorctrl:c-s
a=confid:0
a=userid:0
a=floorid:0
`,
			wantParams: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.100"),
				Port:         5070,
				FloorCtrl:    "c-s",
				ConferenceID: 0,
				UserID:       0,
				FloorID:      0,
			},
			wantErr:        false,
			wantValidation: []string{"conference ID is 0", "user ID is 0", "floor ID is 0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := ParseSDPParams([]byte(tt.sdp))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSDPParams() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantParams == nil {
				if params != nil {
					t.Errorf("ParseSDPParams() = %v, want nil", params)
				}
				return
			}

			if params == nil {
				t.Errorf("ParseSDPParams() = nil, want %v", tt.wantParams)
				return
			}

			// Compare all fields
			if params.ConnectionIP != tt.wantParams.ConnectionIP {
				t.Errorf("ConnectionIP = %v, want %v", params.ConnectionIP, tt.wantParams.ConnectionIP)
			}
			if params.Port != tt.wantParams.Port {
				t.Errorf("Port = %v, want %v", params.Port, tt.wantParams.Port)
			}
			if params.FloorCtrl != tt.wantParams.FloorCtrl {
				t.Errorf("FloorCtrl = %v, want %v", params.FloorCtrl, tt.wantParams.FloorCtrl)
			}
			if params.ConferenceID != tt.wantParams.ConferenceID {
				t.Errorf("ConferenceID = %v, want %v", params.ConferenceID, tt.wantParams.ConferenceID)
			}
			if params.UserID != tt.wantParams.UserID {
				t.Errorf("UserID = %v, want %v", params.UserID, tt.wantParams.UserID)
			}
			if params.FloorID != tt.wantParams.FloorID {
				t.Errorf("FloorID = %v, want %v", params.FloorID, tt.wantParams.FloorID)
			}
			if params.MediaStream != tt.wantParams.MediaStream {
				t.Errorf("MediaStream = %v, want %v", params.MediaStream, tt.wantParams.MediaStream)
			}
			if params.Setup != tt.wantParams.Setup {
				t.Errorf("Setup = %v, want %v", params.Setup, tt.wantParams.Setup)
			}
			if params.Connection != tt.wantParams.Connection {
				t.Errorf("Connection = %v, want %v", params.Connection, tt.wantParams.Connection)
			}

			// Test validation if expected
			if len(tt.wantValidation) > 0 {
				warnings := ValidateSDPParams(params)
				if len(warnings) == 0 {
					t.Errorf("ValidateSDPParams() returned no warnings, expected %d warnings", len(tt.wantValidation))
				}
			}
		})
	}
}

func TestSDPParamsServerAddr(t *testing.T) {
	tests := []struct {
		name   string
		params *SDPParams
		want   string
	}{
		{
			name: "valid params",
			params: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.104"),
				Port:         16886,
			},
			want: "192.168.0.104:16886",
		},
		{
			name:   "nil params",
			params: nil,
			want:   "",
		},
		{
			name: "invalid IP",
			params: &SDPParams{
				ConnectionIP: netip.Addr{},
				Port:         5070,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.params.ServerAddr()
			if got != tt.want {
				t.Errorf("ServerAddr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateSDPParams(t *testing.T) {
	tests := []struct {
		name         string
		params       *SDPParams
		wantWarnings int
	}{
		{
			name: "valid params",
			params: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.104"),
				Port:         16886,
				FloorCtrl:    "c-s",
				ConferenceID: 1,
				UserID:       2,
				FloorID:      1,
				Setup:        "actpass",
			},
			wantWarnings: 0,
		},
		{
			name: "zero IDs",
			params: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.104"),
				Port:         16886,
				FloorCtrl:    "c-s",
				ConferenceID: 0,
				UserID:       0,
				FloorID:      0,
			},
			wantWarnings: 3, // confID, userID, floorID
		},
		{
			name: "invalid floor control",
			params: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.104"),
				Port:         16886,
				FloorCtrl:    "invalid",
				ConferenceID: 1,
				UserID:       1,
				FloorID:      1,
			},
			wantWarnings: 1,
		},
		{
			name: "invalid setup",
			params: &SDPParams{
				ConnectionIP: netip.MustParseAddr("192.168.0.104"),
				Port:         16886,
				Setup:        "invalid-setup",
				ConferenceID: 1,
				UserID:       1,
				FloorID:      1,
			},
			wantWarnings: 1,
		},
		{
			name:         "nil params",
			params:       nil,
			wantWarnings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := ValidateSDPParams(tt.params)
			if len(warnings) != tt.wantWarnings {
				t.Errorf("ValidateSDPParams() returned %d warnings, want %d. Warnings: %v",
					len(warnings), tt.wantWarnings, warnings)
			}
		})
	}
}

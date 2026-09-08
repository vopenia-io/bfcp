package bfcp

import (
	"encoding/binary"
	"fmt"
)

// headerFlagResponse is the R bit of the common header first octet (RFC 8855
// section 5.1), kept in Message.Reserved.
const headerFlagResponse = 0x10

// IsResponse reports whether the R flag is set.
func (m *Message) IsResponse() bool {
	return m.Reserved&headerFlagResponse != 0
}

// SetResponse sets or clears the R flag.
func (m *Message) SetResponse(response bool) {
	if response {
		m.Reserved |= headerFlagResponse
	} else {
		m.Reserved &^= headerFlagResponse
	}
}

// FloorRequestStatusInfo is one FLOOR-REQUEST-STATUS grouped attribute
// (RFC 8855 section 5.2.17).
type FloorRequestStatusInfo struct {
	FloorID       uint16
	Status        RequestStatus
	QueuePosition uint8
	HasStatus     bool
}

// FloorRequestInfo is one FLOOR-REQUEST-INFORMATION grouped attribute
// (RFC 8855 section 5.2.15).
type FloorRequestInfo struct {
	FloorRequestID uint16
	OverallStatus  RequestStatus
	QueuePosition  uint8
	HasOverall     bool
	Floors         []FloorRequestStatusInfo
	BeneficiaryID  uint16
	HasBeneficiary bool
	RequestedByID  uint16
	HasRequestedBy bool
	Priority       Priority
	HasPriority    bool
}

// Status returns the overall status when present, else the first floor status.
func (f FloorRequestInfo) Status() (RequestStatus, bool) {
	if f.HasOverall {
		return f.OverallStatus, true
	}
	for _, fl := range f.Floors {
		if fl.HasStatus {
			return fl.Status, true
		}
	}
	return 0, false
}

// FloorRequestInfos parses every FLOOR-REQUEST-INFORMATION attribute of the
// message.
func (m *Message) FloorRequestInfos() []FloorRequestInfo {
	var infos []FloorRequestInfo
	for i := range m.Attributes {
		attr := &m.Attributes[i]
		if attr.Type != AttrFloorRequestInfo {
			continue
		}
		value := attr.Value
		if attr.RawTLV {
			if len(value) < 2 {
				continue
			}
			value = value[2:]
		}
		if info, err := parseFloorRequestInfo(value); err == nil {
			infos = append(infos, info)
		}
	}
	return infos
}

func parseFloorRequestInfo(value []byte) (FloorRequestInfo, error) {
	var info FloorRequestInfo
	if len(value) < 2 {
		return info, fmt.Errorf("FLOOR-REQUEST-INFORMATION shorter than its header")
	}
	info.FloorRequestID = binary.BigEndian.Uint16(value[:2])
	subs, err := parseTLVs(value[2:])
	if err != nil {
		return info, err
	}
	for _, sub := range subs {
		switch sub.Type {
		case AttrOverallRequestStatus:
			if len(sub.Value) < 2 {
				continue
			}
			if status, pos, ok := findRequestStatus(sub.Value[2:]); ok {
				info.OverallStatus, info.QueuePosition, info.HasOverall = status, pos, true
			}
		case AttrFloorRequestStatus:
			if len(sub.Value) < 2 {
				continue
			}
			fl := FloorRequestStatusInfo{FloorID: binary.BigEndian.Uint16(sub.Value[:2])}
			fl.Status, fl.QueuePosition, fl.HasStatus = findRequestStatus(sub.Value[2:])
			info.Floors = append(info.Floors, fl)
		case AttrBeneficiaryInfo:
			if len(sub.Value) >= 2 {
				info.BeneficiaryID, info.HasBeneficiary = binary.BigEndian.Uint16(sub.Value[:2]), true
			}
		case AttrRequestedByInfo:
			if len(sub.Value) >= 2 {
				info.RequestedByID, info.HasRequestedBy = binary.BigEndian.Uint16(sub.Value[:2]), true
			}
		case AttrPriority:
			if len(sub.Value) >= 2 {
				info.Priority, info.HasPriority = Priority(binary.BigEndian.Uint16(sub.Value[:2])), true
			}
		}
	}
	return info, nil
}

func findRequestStatus(data []byte) (RequestStatus, uint8, bool) {
	subs, err := parseTLVs(data)
	if err != nil {
		return 0, 0, false
	}
	for _, sub := range subs {
		if sub.Type == AttrRequestStatus && len(sub.Value) >= 2 {
			return RequestStatus(sub.Value[0]), sub.Value[1], true
		}
	}
	return 0, 0, false
}

// parseTLVs splits a run of attributes (RFC 8855 section 5.2 length rules).
func parseTLVs(data []byte) ([]Attribute, error) {
	var attrs []Attribute
	offset := 0
	for offset+2 <= len(data) {
		attrType := AttributeType(data[offset] >> 1)
		length := int(data[offset+1])
		if length < 2 || offset+length > len(data) {
			return attrs, fmt.Errorf("invalid attribute length %d at offset %d", length, offset)
		}
		value := make([]byte, length-2)
		copy(value, data[offset+2:offset+length])
		attrs = append(attrs, Attribute{Type: attrType, Length: uint8(length - 2), Value: value})
		offset += length
		if padding := length % 4; padding != 0 {
			offset += 4 - padding
		}
	}
	return attrs, nil
}
